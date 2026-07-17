package tools

import (
	"fmt"
	"net/url"
	"os"
	"os/user"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/text/unicode/norm"
)

const narrowNoBreakSpace = "\u202f"

var macOSScreenshotTime = regexp.MustCompile(` (?i:(AM|PM))\.`)

// ExpandPath applies the path-input normalization used by the built-in tools.
func ExpandPath(filePath string) (string, error) {
	return expandPath(filePath, true, true)
}

func expandPath(filePath string, normalizeSpaces, stripAtPrefix bool) (string, error) {
	if normalizeSpaces {
		filePath = normalizeUnicodeSpaces(filePath)
	}
	if stripAtPrefix && strings.HasPrefix(filePath, "@") {
		filePath = filePath[1:]
	}
	if filePath == "~" || strings.HasPrefix(filePath, "~/") || (runtime.GOOS == "windows" && strings.HasPrefix(filePath, `~\`)) {
		if home, err := toolUserHomeDir(); err == nil {
			if filePath == "~" {
				return home, nil
			}
			return filepath.Join(home, filePath[2:]), nil
		}
	}
	if strings.HasPrefix(filePath, "file://") {
		return fileURLPath(filePath)
	}
	return filePath, nil
}

func toolUserHomeDir() (string, error) {
	if home, err := os.UserHomeDir(); err == nil {
		return home, nil
	}
	current, err := user.Current()
	if err != nil {
		return "", err
	}
	return current.HomeDir, nil
}

// ResolveToCwd resolves a normalized tool path relative to cwd.
func ResolveToCwd(filePath, cwd string) (string, error) {
	var err error
	filePath, err = expandPath(filePath, true, true)
	if err != nil {
		return "", err
	}
	cwd, err = expandPath(cwd, false, false)
	if err != nil {
		return "", err
	}
	if filepath.IsAbs(filePath) {
		return filepath.Clean(filePath), nil
	}
	if absolute, err := filepath.Abs(cwd); err == nil {
		cwd = absolute
	}
	return filepath.Clean(filepath.Join(cwd, filePath)), nil
}

// PathExists reports whether a filesystem entry exists at filePath.
func PathExists(filePath string) bool {
	_, err := os.Stat(filePath)
	return err == nil
}

// ResolveReadPath adds the filename fallbacks used for macOS-generated files.
func ResolveReadPath(filePath, cwd string) (string, error) {
	resolved, err := ResolveToCwd(filePath, cwd)
	if err != nil {
		return "", err
	}
	if PathExists(resolved) {
		return resolved, nil
	}

	variants := []string{
		macOSScreenshotTime.ReplaceAllString(resolved, narrowNoBreakSpace+"$1."),
		norm.NFD.String(resolved),
		strings.ReplaceAll(resolved, "'", "\u2019"),
	}
	variants = append(variants, strings.ReplaceAll(variants[1], "'", "\u2019"))
	for _, variant := range variants {
		if variant != resolved && PathExists(variant) {
			return variant, nil
		}
	}
	return resolved, nil
}

func fileURLPath(value string) (string, error) {
	value = strings.TrimRightFunc(value, func(character rune) bool { return character <= 0x20 })
	value = strings.NewReplacer("\t", "", "\r", "", "\n", "", "\\", "/").Replace(value)
	rest := value[len("file://"):]
	hostEnd := strings.IndexAny(rest, "/?#")
	if hostEnd == -1 {
		hostEnd = len(rest)
	}
	rawHost := rest[:hostEnd]
	decodedHost, err := url.PathUnescape(rawHost)
	if err != nil || !utf8.ValidString(decodedHost) || strings.ContainsAny(decodedHost, "%/\\?#") {
		return "", upstreamToolError("Invalid URL")
	}
	value = "file://" + decodedHost + escapeEmbeddedURLControls(rest[hostEnd:])
	parsed, err := url.Parse(value)
	if err != nil {
		if strings.Contains(err.Error(), "invalid URL escape") {
			return "", upstreamToolError("URI malformed")
		}
		return "", upstreamToolError("Invalid URL")
	}
	if parsed.User != nil || parsed.Port() != "" {
		return "", upstreamToolError("Invalid URL")
	}
	if parsed.Host != "" && !strings.EqualFold(normalizeFileURLHost(parsed.Host), "localhost") {
		return "", upstreamToolErrorf("File URL host must be \"localhost\" or empty on %s", runtime.GOOS)
	}
	if !utf8.ValidString(parsed.Path) {
		return "", upstreamToolError("URI malformed")
	}
	rawPath := parsed.EscapedPath()
	if strings.Contains(strings.ToLower(rawPath), "%2f") {
		return "", upstreamToolError("File URL path must not include encoded / characters")
	}
	if parsed.Path == "" {
		return string(filepath.Separator), nil
	}
	return filepath.FromSlash(parsed.Path), nil
}

func normalizeFileURLHost(host string) string {
	removeIgnored := func(character rune) rune {
		if isUTS46IgnoredHostRune(character) {
			return -1
		}
		return character
	}
	host = strings.Map(removeIgnored, host)
	host = norm.NFKC.String(host)
	return strings.Map(removeIgnored, host)
}

func isUTS46IgnoredHostRune(character rune) bool {
	switch {
	case character == '\u00ad', character == '\u034f', character == '\u200b', character == '\u3164', character == '\ufeff', character == '\uffa0':
		return true
	case character >= '\u115f' && character <= '\u1160':
		return true
	case character >= '\u17b4' && character <= '\u17b5':
		return true
	case character >= '\u180b' && character <= '\u180f':
		return true
	case character >= '\u2060' && character <= '\u2064':
		return true
	case character >= '\u206a' && character <= '\u206f':
		return true
	case unicode.Is(unicode.Variation_Selector, character):
		return true
	case character >= '\U0001bca0' && character <= '\U0001bca3':
		return true
	case character >= '\U0001d173' && character <= '\U0001d17a':
		return true
	}
	return false
}

func escapeEmbeddedURLControls(value string) string {
	var escaped strings.Builder
	for index := 0; index < len(value); index++ {
		if value[index] < 0x20 || value[index] == 0x7f {
			escaped.WriteString(fmt.Sprintf("%%%02X", value[index]))
			continue
		}
		escaped.WriteByte(value[index])
	}
	return escaped.String()
}

func normalizeUnicodeSpaces(value string) string {
	return strings.Map(func(char rune) rune {
		switch {
		case char == '\u00a0', char >= '\u2000' && char <= '\u200a', char == '\u202f', char == '\u205f', char == '\u3000':
			return ' '
		default:
			return char
		}
	}, value)
}
