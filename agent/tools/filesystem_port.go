package tools

import (
	"context"
	"errors"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"unicode"

	"github.com/OrdalieTech/orb/engine/harness"
	"github.com/bmatcuk/doublestar/v4"
)

// FileSystemToolsOptions routes the read, write, edit, ls and find tools
// through a harness.FileSystem port, reporting the Node error codes the
// native operations report. Grep is absent: its search runs ripgrep and has
// no port-backed seam. The port cannot check permission bits, so Access only
// proves existence.
func FileSystemToolsOptions(fsys harness.FileSystem) *ToolsOptions {
	operations := fileSystemOperations{fsys: fsys}
	return &ToolsOptions{
		Read:  &ReadToolOptions{Operations: operations},
		Write: &WriteToolOptions{Operations: operations},
		Edit:  &EditToolOptions{Operations: operations},
		Ls:    &LsToolOptions{Operations: operations},
		Find:  &FindToolOptions{Operations: operations},
	}
}

type fileSystemOperations struct{ fsys harness.FileSystem }

func fileErrorCode(err error) harness.FileErrorCode {
	var typed *harness.FileError
	if errors.As(err, &typed) {
		return typed.Code
	}
	return ""
}

// portError reports a port failure as the native operation's Node error;
// codes without a Node errno pass through unchanged.
func portError(operation, path string, err error) error {
	if err == nil {
		return nil
	}
	code := map[harness.FileErrorCode]string{
		harness.FileErrorNotFound:         "ENOENT",
		harness.FileErrorPermissionDenied: "EACCES",
		harness.FileErrorNotDirectory:     "ENOTDIR",
		harness.FileErrorIsDirectory:      "EISDIR",
		harness.FileErrorInvalid:          "EINVAL",
	}[fileErrorCode(err)]
	switch {
	case fileErrorCode(err) == harness.FileErrorAborted:
		return errOperationAborted
	case code == "":
		return err
	case operation == "read":
		path = ""
	}
	return nodeFilesystemError{code: code, operation: operation, path: path}
}

// readError mirrors fs.readFile: opening a directory succeeds and the read fails.
func readError(path string, err error) error {
	if fileErrorCode(err) == harness.FileErrorIsDirectory {
		return portError("read", path, err)
	}
	return portError("open", path, err)
}

// stat follows symlinks like fs.stat.
func (operations fileSystemOperations) stat(ctx context.Context, path string) (harness.FileInfo, error) {
	canonical, err := operations.fsys.CanonicalPath(ctx, path)
	if err != nil {
		return harness.FileInfo{}, err
	}
	return operations.fsys.FileInfo(ctx, canonical)
}

func (operations fileSystemOperations) ReadFile(ctx context.Context, path string) ([]byte, error) {
	if err := nodeNullPathError(path); err != nil {
		return nil, err
	}
	data, err := operations.fsys.ReadBinaryFile(ctx, path)
	return data, readError(path, err)
}

func (operations fileSystemOperations) Access(ctx context.Context, path string) error {
	if err := nodeNullPathError(path); err != nil {
		return err
	}
	_, err := operations.fsys.CanonicalPath(ctx, path)
	return portError("access", path, err)
}

func (operations fileSystemOperations) DetectImageMimeType(ctx context.Context, path string) (string, error) {
	data, err := operations.fsys.ReadBinaryFile(ctx, path)
	if err != nil {
		return "", readError(path, err)
	}
	return DetectSupportedImageMimeType(data[:min(len(data), 4100)]), nil
}

func (operations fileSystemOperations) WriteFile(ctx context.Context, path, content string) error {
	return portError("open", path, operations.fsys.WriteFile(ctx, path, []byte(encodeNodeUTF8(content))))
}

func (operations fileSystemOperations) MkdirAll(ctx context.Context, path string) error {
	if info, err := operations.stat(ctx, path); err == nil && info.Kind != harness.FileKindDirectory {
		return nodeFilesystemError{code: "EEXIST", operation: "mkdir", path: path}
	}
	return portError("mkdir", path, operations.fsys.CreateDir(ctx, path, true))
}

func (operations fileSystemOperations) Exists(ctx context.Context, path string) (bool, error) {
	_, err := operations.stat(ctx, path)
	return err == nil, nil
}

func (operations fileSystemOperations) Stat(ctx context.Context, path string) (LsPathStat, error) {
	info, err := operations.stat(ctx, path)
	if err != nil {
		return LsPathStat{}, portError("stat", path, err)
	}
	return LsPathStat{Directory: info.Kind == harness.FileKindDirectory}, nil
}

func (operations fileSystemOperations) ReadDir(ctx context.Context, path string) ([]string, error) {
	entries, err := operations.fsys.ListDir(ctx, path)
	if err != nil {
		return nil, portError("scandir", path, err)
	}
	names := make([]string, len(entries))
	for index, entry := range entries {
		names[index] = decodeNodeUTF8([]byte(entry.Name))
	}
	return names, nil
}

// Glob walks the port the way the native tool drives fd: hidden entries are
// included, directories match with a trailing slash, symlinks are not
// followed, a pattern containing "/" matches the full path (with an implicit
// leading "**/"), and matching is smart-case. Ignore files such as
// .gitignore are not consulted.
func (operations fileSystemOperations) Glob(ctx context.Context, pattern, searchPath string, options FindGlobOptions) ([]string, error) {
	fullPath := strings.Contains(pattern, "/")
	if fullPath && !strings.HasPrefix(pattern, "/") && !strings.HasPrefix(pattern, "**/") && pattern != "**" {
		pattern = "**/" + pattern
	}
	foldCase := !strings.ContainsFunc(pattern, unicode.IsUpper)
	if foldCase {
		pattern = strings.ToLower(pattern)
	}
	results := []string{}
	var walk func(dir, relative string) error
	walk = func(dir, relative string) error {
		if err := checkAborted(ctx); err != nil {
			return err
		}
		entries, err := operations.fsys.ListDir(ctx, dir)
		if err != nil {
			return err
		}
		slices.SortFunc(entries, func(left, right harness.FileInfo) int { return strings.Compare(left.Name, right.Name) })
		for _, entry := range entries {
			child := path.Join(relative, entry.Name)
			if slices.ContainsFunc(options.Ignore, func(ignore string) bool { return matchGlob(ignore, child) }) {
				continue
			}
			candidate := entry.Name
			if fullPath {
				candidate = filepath.ToSlash(entry.Path)
			}
			if foldCase {
				candidate = strings.ToLower(candidate)
			}
			isDir := entry.Kind == harness.FileKindDirectory
			if matchGlob(pattern, candidate) {
				match := filepath.Join(searchPath, filepath.FromSlash(child))
				if isDir {
					match += "/"
				}
				results = append(results, match)
				if options.Limit > 0 && float64(len(results)) >= options.Limit {
					return errGlobLimit
				}
			}
			if isDir {
				if err := walk(entry.Path, child); errors.Is(err, errGlobLimit) || errors.Is(err, errOperationAborted) {
					return err
				}
			}
		}
		return nil
	}
	if err := walk(searchPath, ""); err != nil && !errors.Is(err, errGlobLimit) {
		if errors.Is(err, errOperationAborted) {
			return nil, err
		}
		return nil, portError("scandir", searchPath, err)
	}
	return results, nil
}

var errGlobLimit = errors.New("glob limit reached")

func matchGlob(pattern, name string) bool {
	matched, err := doublestar.Match(pattern, name)
	return err == nil && matched
}
