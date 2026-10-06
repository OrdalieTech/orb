package tools

import (
	"os"
	"strings"

	"golang.org/x/text/unicode/norm"

	"github.com/OrdalieTech/orb/internal/lazyregexp"
	"github.com/OrdalieTech/orb/internal/nodepath"
)

const narrowNoBreakSpace = "\u202f"

var macOSScreenshotTime = lazyregexp.New(` (?i:(AM|PM))\.`)

// ResolveToCwd resolves a normalized tool path relative to cwd.
func ResolveToCwd(filePath, cwd string) (string, error) {
	filePath, err := nodepath.Expand(nodepath.NormalizeShellPath(strings.TrimPrefix(nodepath.NormalizeUnicodeSpaces(filePath), "@")))
	if err != nil {
		return "", err
	}
	if cwd, err = nodepath.Expand(nodepath.NormalizeShellPath(cwd)); err != nil {
		return "", err
	}
	return nodepath.Resolve(filePath, cwd), nil
}

// PathExists reports whether a filesystem entry exists at filePath.
func PathExists(filePath string) bool {
	_, err := os.Stat(filePath)
	return err == nil
}

// ResolveReadPath adds the filename fallbacks used for macOS-generated files.
func ResolveReadPath(filePath, cwd string) (string, error) {
	return resolveReadPath(filePath, cwd, PathExists)
}

// resolveReadPath checks the fallbacks with exists, the filesystem the read
// itself goes through.
func resolveReadPath(filePath, cwd string, exists func(string) bool) (string, error) {
	resolved, err := ResolveToCwd(filePath, cwd)
	if err != nil {
		return "", err
	}
	if exists(resolved) {
		return resolved, nil
	}

	variants := []string{
		macOSScreenshotTime().ReplaceAllString(resolved, narrowNoBreakSpace+"$1."),
		norm.NFD.String(resolved),
		strings.ReplaceAll(resolved, "'", "\u2019"),
	}
	variants = append(variants, strings.ReplaceAll(variants[1], "'", "\u2019"))
	for _, variant := range variants {
		if variant != resolved && exists(variant) {
			return variant, nil
		}
	}
	return resolved, nil
}
