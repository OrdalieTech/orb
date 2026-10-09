// Package nodepath converts path inputs the way upstream's Node runtime does
// on the platform orb runs on: url.fileURLToPath, url.pathToFileURL, and the
// win32 Git Bash/MSYS drive-path rewrite from upstream utils/paths.ts.
package nodepath

import (
	"errors"
	"net/netip"
	"net/url"
	"os"
	"os/user"
	"path/filepath"
	"runtime"
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/text/unicode/norm"
)

const windows = runtime.GOOS == "windows"

// Node's error messages for rejected file URLs (ERR_INVALID_URL, URIError,
// ERR_INVALID_FILE_URL_PATH); their text is observable, capitals included.
var (
	ErrInvalidURL   = errors.New("Invalid URL") //nolint:staticcheck // Node's error text.
	ErrURIMalformed = errors.New("URI malformed")
	errNotAbsolute  = errors.New("File URL path must be absolute") //nolint:staticcheck // Node's error text.
)

// FileURLToPath parses raw with the WHATWG rules for the file scheme and
// converts it like Node's url.fileURLToPath: POSIX rejects hosts, win32 maps
// them to UNC paths and requires a drive letter otherwise.
func FileURLToPath(raw string) (string, error) {
	return fileURLToPath(raw, windows)
}

// PathToFileURL encodes an absolute native path like Node's url.pathToFileURL.
func PathToFileURL(path string) string {
	return pathToFileURL(path, windows)
}

// NormalizeShellPath rewrites Git Bash, MSYS, Cygwin and WSL drive paths
// ("/c/x", "/mnt/c/x", "/cygdrive/c/x") to native drive paths on win32 and
// returns path unchanged elsewhere.
func NormalizeShellPath(path string) string {
	if !windows {
		return path
	}
	return windowsShellPath(path)
}

// Expand resolves a leading ~ against the user's home, when there is one, and
// converts a file:// URL to its path.
func Expand(path string) (string, error) {
	if path == "~" || strings.HasPrefix(path, "~/") || windows && strings.HasPrefix(path, `~\`) {
		home, err := HomeDir()
		if err != nil {
			return "", err
		}
		return filepath.Join(home, path[1:]), nil
	}
	if strings.HasPrefix(path, "file://") {
		return FileURLToPath(path)
	}
	return path, nil
}

// Normalize is upstream's normalizePath: the shell-drive rewrite, then Expand;
// a malformed file URL stays an ordinary path.
func Normalize(path string) string {
	path = NormalizeShellPath(path)
	if expanded, err := Expand(path); err == nil {
		return expanded
	}
	return path
}

// Resolve normalizes path and makes it absolute against base, like Node's
// path.resolve(base, path).
func Resolve(path, base string) string {
	path = Normalize(path)
	if filepath.IsAbs(path) {
		return filepath.Clean(path)
	}
	if absolute, err := filepath.Abs(base); err == nil {
		base = absolute
	}
	// Node's win32 path.resolve roots "\x" and "/x" on the base's drive.
	if windows && (strings.HasPrefix(path, `\`) || strings.HasPrefix(path, "/")) {
		return filepath.Clean(filepath.VolumeName(base) + path)
	}
	return filepath.Join(base, path)
}

// AgentDirEnv names the variable that moves Orb's agent directory.
const AgentDirEnv = "ORB_AGENT_DIR"

// AgentDir is Orb's agent directory: configured, the value of
// AgentDirEnv, when it is set, else ~/.orb/agent.
func AgentDir(configured string) (string, error) {
	if configured != "" {
		return Expand(NormalizeShellPath(configured))
	}
	home, err := HomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".orb", "agent"), nil
}

// HomeDir is the user's home directory, from the account database when $HOME is unset.
func HomeDir() (string, error) {
	if home, err := os.UserHomeDir(); err == nil {
		return home, nil
	}
	current, err := user.Current()
	if err != nil {
		return "", err
	}
	return current.HomeDir, nil
}

// NormalizeUnicodeSpaces folds pasted path spacing without changing other whitespace.
func NormalizeUnicodeSpaces(value string) string {
	return strings.Map(func(char rune) rune {
		switch {
		case char == '\u00a0', char >= '\u2000' && char <= '\u200a', char == '\u202f', char == '\u205f', char == '\u3000':
			return ' '
		default:
			return char
		}
	}, value)
}

func windowsShellPath(path string) string {
	if !strings.HasPrefix(path, "/") || strings.HasPrefix(path, "//") || strings.Contains(path, `\`) {
		return path
	}
	rest := path[1:]
	lower := strings.ToLower(rest)
	switch {
	case strings.HasPrefix(lower, "mnt/"):
		rest = rest[len("mnt/"):]
	case strings.HasPrefix(lower, "cygdrive/"):
		rest = rest[len("cygdrive/"):]
	}
	drive, suffix, _ := strings.Cut(rest, "/")
	if len(drive) != 1 || !isASCIIAlpha(drive[0]) {
		return path
	}
	return strings.ToUpper(drive) + `:\` + strings.ReplaceAll(suffix, "/", `\`)
}

func fileURLToPath(raw string, windows bool) (string, error) {
	host, pathname, err := parseFileURL(raw)
	if err != nil {
		return "", err
	}
	return hostPathToPath(host, pathname, windows)
}

// parseFileURL returns the serialized host and pathname WHATWG gives a file
// URL. Characters the parser would percent-encode are left raw: every caller
// percent-decodes the pathname afterwards, so only existing escapes matter.
func parseFileURL(raw string) (string, string, error) {
	raw = strings.TrimFunc(raw, func(character rune) bool { return character <= 0x20 })
	raw = strings.NewReplacer("\t", "", "\n", "", "\r", "").Replace(raw)
	scheme, rest, ok := strings.Cut(raw, ":")
	if !ok || !strings.EqualFold(scheme, "file") {
		return "", "", ErrInvalidURL
	}
	if end := strings.IndexAny(rest, "?#"); end >= 0 {
		rest = rest[:end]
	}
	rest = strings.ReplaceAll(rest, `\`, "/")
	host := ""
	if strings.HasPrefix(rest, "//") {
		rest = rest[2:]
		end := strings.IndexByte(rest, '/')
		if end < 0 {
			end = len(rest)
		}
		host, rest = rest[:end], rest[end:]
		if isWindowsDriveLetter(host) {
			host, rest = "", "/"+host+rest
		}
	}
	host, err := parseFileHost(host)
	if err != nil {
		return "", "", err
	}
	return host, normalizeFilePath(rest), nil
}

func parseFileHost(host string) (string, error) {
	if host == "" {
		return "", nil
	}
	if strings.HasPrefix(host, "[") {
		address, err := netip.ParseAddr(strings.TrimSuffix(host[1:], "]"))
		if !strings.HasSuffix(host, "]") || err != nil || !address.Is6() {
			return "", ErrInvalidURL
		}
		return "[" + address.String() + "]", nil
	}
	decoded, err := url.PathUnescape(host)
	if err != nil || !utf8.ValidString(decoded) {
		return "", ErrInvalidURL
	}
	// UTS #46 drops ignorable code points and applies NFKC before the host is checked.
	decoded = strings.Map(dropIgnoredHostRune, norm.NFKC.String(strings.Map(dropIgnoredHostRune, decoded)))
	for index := 0; index < len(decoded); index++ {
		if decoded[index] <= 0x20 || decoded[index] == 0x7f || strings.IndexByte("#%/:<>?@[\\]^|", decoded[index]) >= 0 {
			return "", ErrInvalidURL
		}
	}
	decoded = strings.ToLower(decoded)
	if decoded == "localhost" {
		return "", nil
	}
	return decoded, nil
}

func dropIgnoredHostRune(character rune) rune {
	switch {
	case character == '\u00ad', character == '\u034f', character == '\u200b', character == '\u3164', character == '\ufeff', character == '\uffa0',
		character >= '\u115f' && character <= '\u1160', character >= '\u17b4' && character <= '\u17b5',
		character >= '\u180b' && character <= '\u180f', character >= '\u2060' && character <= '\u2064',
		character >= '\u206a' && character <= '\u206f', unicode.Is(unicode.Variation_Selector, character),
		character >= '\U0001bca0' && character <= '\U0001bca3', character >= '\U0001d173' && character <= '\U0001d17a':
		return -1
	}
	return character
}

// normalizeFilePath applies WHATWG path-state dot-segment and drive-letter
// rules; ".." never climbs above a leading drive letter in file URLs.
func normalizeFilePath(path string) string {
	segments := strings.Split(strings.TrimPrefix(path, "/"), "/")
	var output []string
	for index, segment := range segments {
		last := index == len(segments)-1
		switch lower := strings.ToLower(segment); lower {
		case ".", "%2e":
			if last {
				output = append(output, "")
			}
		case "..", ".%2e", "%2e.", "%2e%2e":
			if len(output) > 1 || len(output) == 1 && !isNormalizedDriveLetter(output[0]) {
				output = output[:len(output)-1]
			}
			if last {
				output = append(output, "")
			}
		default:
			if len(output) == 0 && isWindowsDriveLetter(segment) {
				segment = segment[:1] + ":"
			}
			output = append(output, segment)
		}
	}
	return "/" + strings.Join(output, "/")
}

func hostPathToPath(host, pathname string, windows bool) (string, error) {
	lower := strings.ToLower(pathname)
	if !windows {
		if host != "" {
			return "", errors.New(`File URL host must be "localhost" or empty on ` + runtime.GOOS) //nolint:staticcheck // Node's error text.
		}
		if strings.Contains(lower, "%2f") {
			return "", errors.New("File URL path must not include encoded / characters") //nolint:staticcheck // Node's error text.
		}
		return decodeURIComponent(pathname)
	}
	if strings.Contains(lower, "%2f") || strings.Contains(lower, "%5c") {
		return "", errors.New(`File URL path must not include encoded \ or / characters`) //nolint:staticcheck // Node's error text.
	}
	decoded, err := decodeURIComponent(strings.ReplaceAll(pathname, "/", `\`))
	if err != nil {
		return "", err
	}
	// A non-ASCII server name would need WHATWG's IDNA conversion, which is not ported.
	if strings.ContainsFunc(host, func(character rune) bool { return character >= utf8.RuneSelf }) {
		return "", ErrInvalidURL
	}
	if host != "" {
		return `\\` + host + decoded, nil
	}
	if len(decoded) < 3 || !isASCIIAlpha(decoded[1]) || decoded[2] != ':' {
		return "", errNotAbsolute
	}
	return decoded[1:], nil
}

func decodeURIComponent(value string) (string, error) {
	decoded, err := url.PathUnescape(value)
	if err != nil || !utf8.ValidString(decoded) {
		return "", ErrURIMalformed
	}
	return decoded, nil
}

func pathToFileURL(path string, windows bool) string {
	host := ""
	if windows {
		if strings.HasPrefix(path, `\\`) {
			rest := strings.TrimPrefix(path[2:], `?\UNC\`)
			if end := strings.IndexByte(rest, '\\'); end > 0 {
				host, path = strings.ToLower(rest[:end]), rest[end:]
			}
		}
		path = strings.ReplaceAll(path, `\`, "/")
		if !strings.HasPrefix(path, "/") {
			path = "/" + path
		}
	}
	var encoded strings.Builder
	encoded.WriteString("file://")
	encoded.WriteString(host)
	const hex = "0123456789ABCDEF"
	for _, unit := range []byte(path) {
		switch {
		case unit < 0x20 || unit == 0x7f || unit >= 0x80,
			unit == ' ', unit == '"', unit == '#', unit == '<', unit == '>',
			unit == '?', unit == '`', unit == '{', unit == '}', unit == '^',
			unit == '|', unit == '\\', unit == '%':
			encoded.WriteByte('%')
			encoded.WriteByte(hex[unit>>4])
			encoded.WriteByte(hex[unit&0x0f])
		default:
			encoded.WriteByte(unit)
		}
	}
	return encoded.String()
}

func isWindowsDriveLetter(value string) bool {
	return len(value) == 2 && isASCIIAlpha(value[0]) && (value[1] == ':' || value[1] == '|')
}

func isNormalizedDriveLetter(value string) bool {
	return len(value) == 2 && isASCIIAlpha(value[0]) && value[1] == ':'
}

func isASCIIAlpha(character byte) bool {
	return character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z'
}
