// Package nodepath converts path inputs the way upstream's Node runtime does
// on the platform orb runs on: url.fileURLToPath, url.pathToFileURL, and the
// win32 Git Bash/MSYS drive-path rewrite from upstream utils/paths.ts.
package nodepath

import (
	"errors"
	"net/netip"
	"net/url"
	"runtime"
	"strings"
	"unicode/utf8"
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

// HostPathToPath converts the host and percent-encoded pathname of an already
// parsed file URL; host must already be "" for localhost.
func HostPathToPath(host, pathname string) (string, error) {
	return hostPathToPath(host, pathname, windows)
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
