package harness_test

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/OrdalieTech/orb/conformance/runner"
	agentharness "github.com/OrdalieTech/orb/engine/harness"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

type f6HarnessFixture struct {
	SchemaVersion int            `json:"schemaVersion"`
	Env           map[string]any `json:"env"`
}

func TestExecutionEnvironmentPreservesV0842Behavior(t *testing.T) {
	fixture := loadF6HarnessFixture(t)
	root := t.TempDir()
	env := agentharness.NodeExecutionEnv{CWD: root, ShellEnv: map[string]string{"BASE_VALUE": "base"}}
	if err := env.WriteFile(context.Background(), "nested/lines.txt", []byte("one\r\ntwo\nthree\n")); err != nil {
		t.Fatal(err)
	}
	if err := env.WriteFile(context.Background(), "target.txt", []byte{0, 1, 2, 255}); err != nil {
		t.Fatal(err)
	}
	if err := env.CreateDir(context.Background(), "empty-remove", false); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("target.txt", filepath.Join(root, "target-link")); err != nil {
		t.Fatal(err)
	}

	abs, absErr := env.AbsolutePath(context.Background(), "nested/../target.txt")
	absAlready, absAlreadyErr := env.AbsolutePath(context.Background(), "/a/../b")
	joined, joinErr := env.JoinPath(context.Background(), root, "nested", "..", "target.txt")
	lines, linesErr := env.ReadTextLines(context.Background(), "nested/lines.txt", 2)
	negativeLines, negativeLinesErr := env.ReadTextLines(context.Background(), "nested/lines.txt", -1)
	binary, binaryErr := env.ReadBinaryFile(context.Background(), "target.txt")
	link, linkErr := env.FileInfo(context.Background(), "target-link")
	canonical, canonicalErr := env.CanonicalPath(context.Background(), "target-link")
	missing, existsErr := env.Exists(context.Background(), "missing")
	_, missingErr := env.ReadTextFile(context.Background(), "missing")
	_, directoryErr := env.ReadTextFile(context.Background(), "nested")
	_, listErr := env.ListDir(context.Background(), "target.txt")
	emptyDirectoryRemoveErr := env.Remove(context.Background(), "empty-remove", false, true)

	chunks := make([]string, 0, 2)
	execResult, execErr := env.Exec(context.Background(), `printf "out:$BASE_VALUE:$EXTRA"; printf "err" >&2; exit 7`, agentharness.ExecOptions{
		Env:      map[string]string{"EXTRA": "extra"},
		OnStdout: func(chunk string) error { chunks = append(chunks, "stdout:"+chunk); return nil },
		OnStderr: func(chunk string) error { chunks = append(chunks, "stderr:"+chunk); return nil },
	})
	signaledExecResult, signaledExecErr := env.Exec(context.Background(), "kill -9 $$", agentharness.ExecOptions{})
	sort.Strings(chunks)
	aborted, cancel := context.WithCancel(context.Background())
	cancel()
	_, abortErr := env.Exec(aborted, "printf never", agentharness.ExecOptions{})
	zero := 0.0
	_, invalidTimeoutErr := env.Exec(context.Background(), "printf never", agentharness.ExecOptions{TimeoutSeconds: &zero})
	tiny := 0.01
	_, timeoutErr := env.Exec(context.Background(), "sleep 1", agentharness.ExecOptions{TimeoutSeconds: &tiny})
	_, callbackErr := env.Exec(context.Background(), "printf boom", agentharness.ExecOptions{
		OnStdout: func(string) error { return errors.New("callback boom") },
	})
	if err := env.WriteFile(context.Background(), "abort/remove.txt", []byte("remove me")); err != nil {
		t.Fatal(err)
	}
	preAbs, preAbsErr := env.AbsolutePath(aborted, "/a/../b")
	preJoin, preJoinErr := env.JoinPath(aborted, root, "nested", "..", "target.txt")
	_, preReadTextErr := env.ReadTextFile(aborted, "target.txt")
	_, preReadLinesErr := env.ReadTextLines(aborted, "nested/lines.txt", -1)
	_, preReadBinaryErr := env.ReadBinaryFile(aborted, "target.txt")
	preWriteErr := env.WriteFile(aborted, "abort/blocked.txt", []byte("blocked"))
	preAppendErr := env.AppendFile(aborted, "abort/appended.txt", []byte("appended"))
	preInfo, preInfoErr := env.FileInfo(aborted, "target.txt")
	_, preListErr := env.ListDir(aborted, ".")
	preCanonical, preCanonicalErr := env.CanonicalPath(aborted, "target.txt")
	preExists, preExistsErr := env.Exists(aborted, "target.txt")
	preCreateDirErr := env.CreateDir(aborted, "abort/created", true)
	preRemoveErr := env.Remove(aborted, "abort/remove.txt", false, false)
	preTempDir, preTempDirErr := env.CreateTempDir(aborted, "orb-aborted-")
	preTempFile, preTempFileErr := env.CreateTempFile(aborted, "aborted-", ".tmp")
	tempDir, tempDirErr := env.CreateTempDir(context.Background(), "orb-harness-")
	tempFile, tempFileErr := env.CreateTempFile(context.Background(), "pre-", ".tmp")
	for _, created := range []struct {
		path string
		err  error
		file bool
	}{
		{path: preTempDir, err: preTempDirErr},
		{path: preTempFile, err: preTempFileErr, file: true},
		{path: tempDir, err: tempDirErr},
		{path: tempFile, err: tempFileErr, file: true},
	} {
		if created.err != nil || created.path == "" {
			continue
		}
		cleanupPath := created.path
		if created.file {
			cleanupPath = filepath.Dir(cleanupPath)
		}
		t.Cleanup(func() { _ = os.RemoveAll(cleanupPath) })
	}
	tempExists, tempExistsErr := env.Exists(context.Background(), tempFile)
	if err := env.Cleanup(); err != nil {
		t.Fatal(err)
	}

	got := map[string]any{
		"absolutePath":                f6HarnessResult(abs, absErr, root),
		"absolutePathAlreadyAbsolute": f6HarnessResult(absAlready, absAlreadyErr, root),
		"joinPath":                    f6HarnessResult(joined, joinErr, root),
		"readTextLines":               f6HarnessResult(lines, linesErr, root),
		"negativeMaxLines":            f6HarnessResult(negativeLines, negativeLinesErr, root),
		"readBinary":                  f6HarnessResult(f6HarnessBytes(binary), binaryErr, root),
		"symlinkInfo":                 f6HarnessResult(map[string]any{"name": link.Name, "path": runner.NormalizeFixturePath(link.Path, root), "kind": link.Kind, "size": link.Size}, linkErr, root),
		"symlinkCanonical":            f6HarnessResult(canonical, canonicalErr, root),
		"missingExists":               f6HarnessResult(missing, existsErr, root),
		"missingRead":                 f6HarnessResult(nil, missingErr, root),
		"directoryRead":               f6HarnessResult(nil, directoryErr, root),
		"listFile":                    f6HarnessResult(nil, listErr, root),
		"emptyDirectoryRemove":        f6HarnessVoidResult(emptyDirectoryRemoveErr, root),
		"exec":                        f6HarnessResult(execResult, execErr, root),
		"signaledExec":                f6HarnessResult(signaledExecResult, signaledExecErr, root),
		"callbackChunks":              chunks,
		"preAbortedExec":              f6HarnessResult(nil, abortErr, root),
		"preAborted": map[string]any{
			"absolutePath":   f6HarnessResult(preAbs, preAbsErr, root),
			"joinPath":       f6HarnessResult(preJoin, preJoinErr, root),
			"readTextFile":   f6HarnessResult(nil, preReadTextErr, root),
			"readTextLines":  f6HarnessResult(nil, preReadLinesErr, root),
			"readBinaryFile": f6HarnessResult(nil, preReadBinaryErr, root),
			"writeFile":      f6HarnessVoidResult(preWriteErr, root),
			"appendFile":     f6HarnessVoidResult(preAppendErr, root),
			"fileInfo":       f6HarnessStableFileInfoResult(preInfo, preInfoErr, root),
			"listDir":        f6HarnessResult(nil, preListErr, root),
			"canonicalPath":  f6HarnessResult(preCanonical, preCanonicalErr, root),
			"exists":         f6HarnessResult(preExists, preExistsErr, root),
			"createDir":      f6HarnessVoidResult(preCreateDirErr, root),
			"remove":         f6HarnessVoidResult(preRemoveErr, root),
			"createTempDir":  f6HarnessTempResult(preTempDir, preTempDirErr, "orb-aborted-", "", root),
			"createTempFile": f6HarnessTempResult(preTempFile, preTempFileErr, "aborted-", ".tmp", root),
		},
		"invalidTimeout": f6HarnessResult(nil, invalidTimeoutErr, root),
		"timedOutExec":   f6HarnessResult(nil, timeoutErr, root),
		"callbackError":  f6HarnessResult(nil, callbackErr, root),
		"temp": map[string]any{
			"dirPrefix":  tempDirErr == nil && strings.HasPrefix(filepath.Base(tempDir), "orb-harness-"),
			"filePrefix": tempFileErr == nil && strings.HasPrefix(filepath.Base(tempFile), "pre-"),
			"fileSuffix": tempFileErr == nil && strings.HasSuffix(tempFile, ".tmp"),
			"fileExists": tempExistsErr == nil && tempExists,
		},
	}
	assertF6HarnessMap(t, fixture.Env, got)
}
func loadF6HarnessFixture(t *testing.T) f6HarnessFixture {
	t.Helper()
	var fixture f6HarnessFixture
	data, err := os.ReadFile("testdata/env-v0842.json")
	if err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(data, &fixture); err != nil {
		t.Fatal(err)
	}
	if fixture.SchemaVersion != 1 {
		t.Fatalf("F6Harness schema version = %d", fixture.SchemaVersion)
	}
	return fixture
}
func f6HarnessBytes(value []byte) []int {
	result := make([]int, len(value))
	for index := range value {
		result[index] = int(value[index])
	}
	return result
}
func f6HarnessResult(value any, err error, root string) map[string]any {
	if err != nil {
		return map[string]any{"ok": false, "error": f6HarnessTypedError(err, root)}
	}
	return map[string]any{"ok": true, "value": normalizeF6HarnessValue(f6HarnessJSONValue(value), root, "")}
}
func f6HarnessVoidResult(err error, root string) map[string]any {
	if err != nil {
		return map[string]any{"ok": false, "error": f6HarnessTypedError(err, root)}
	}
	return map[string]any{"ok": true}
}
func f6HarnessStableFileInfoResult(info agentharness.FileInfo, err error, root string) map[string]any {
	if err != nil {
		return map[string]any{"ok": false, "error": f6HarnessTypedError(err, root)}
	}
	return f6HarnessResult(map[string]any{
		"name": info.Name, "path": info.Path, "kind": info.Kind, "size": info.Size,
	}, nil, root)
}
func f6HarnessTempResult(pathValue string, err error, prefix, suffix, root string) map[string]any {
	if err != nil {
		return map[string]any{"ok": false, "error": f6HarnessTypedError(err, root)}
	}
	return f6HarnessResult(
		strings.HasPrefix(filepath.Base(pathValue), prefix) && strings.HasSuffix(pathValue, suffix),
		nil,
		root,
	)
}
func f6HarnessTypedError(err error, root string) map[string]any {
	result := map[string]any{"message": runner.NormalizeFixturePath(err.Error(), root)}
	var fileError *agentharness.FileError
	var executionError *agentharness.ExecutionError
	switch {
	case errors.As(err, &fileError):
		result["code"] = fileError.Code
		if fileError.Path != "" {
			result["path"] = runner.NormalizeFixturePath(fileError.Path, root)
		}
	case errors.As(err, &executionError):
		result["code"] = executionError.Code
	}
	return result
}
func f6HarnessJSONValue(value any) any {
	encoded, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	var decoded any
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		panic(err)
	}
	return decoded
}
func normalizeF6HarnessValue(value any, root, redactKey string) any {
	switch typed := value.(type) {
	case string:
		return runner.NormalizeFixturePath(typed, root)
	case []any:
		for index := range typed {
			typed[index] = normalizeF6HarnessValue(typed[index], root, redactKey)
		}
	case map[string]any:
		for key := range typed {
			if redactKey != "" && key == redactKey {
				typed[key] = "<" + redactKey + ">"
			} else {
				typed[key] = normalizeF6HarnessValue(typed[key], root, redactKey)
			}
		}
	}
	return value
}
func assertF6HarnessMap(t *testing.T, want, got map[string]any) {
	t.Helper()
	keys := make([]string, 0, len(want))
	for key := range want {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for key := range got {
		if _, ok := want[key]; !ok {
			t.Errorf("unexpected observation %q", key)
		}
	}
	for _, key := range keys {
		key := key
		t.Run(key, func(t *testing.T) {
			value, ok := got[key]
			if !ok {
				t.Fatalf("missing observation %q", key)
			}
			wantMap, wantIsMap := want[key].(map[string]any)
			gotMap, gotIsMap := value.(map[string]any)
			if wantIsMap && gotIsMap {
				assertF6HarnessMap(t, wantMap, gotMap)
				return
			}
			runner.AssertCanonicalJSONEqual(t, want[key], value, "")
		})
	}
}
