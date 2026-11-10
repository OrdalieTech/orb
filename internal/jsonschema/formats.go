package jsonschema

import (
	"errors"
	"net"
	"net/url"
	"regexp"
	"regexp/syntax"
	"strconv"
	"strings"

	"github.com/OrdalieTech/orb/internal/lazyregexp"
)

// formatChecks are pi-schema's format checks (adapted from TypeBox 1.3.27).
// idn-email and idn-hostname, which need Unicode normalization and IDNA
// tables, are not checked.
var formatChecks = map[string]func(string) bool{
	"date":                      isDate,
	"date-time":                 isDateTime,
	"duration":                  lazy(`^P(?:(?:\d+Y(?:\d+M(?:\d+D)?)?|\d+M(?:\d+D)?|\d+D)(?:T(?:\d+H(?:\d+M(?:\d+S)?)?|\d+M(?:\d+S)?|\d+S))?|T(?:\d+H(?:\d+M(?:\d+S)?)?|\d+M(?:\d+S)?|\d+S)|\d+W)$`),
	"email":                     lazy(`(?i)^(?:[a-z0-9!#$%&'*+/=?^_` + "`" + `{|}~-]+(?:\.[a-z0-9!#$%&'*+/=?^_` + "`" + `{|}~-]+)*|"(?:[^"\\]|\\[\x20-\x7e])*")@(?:[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?(?:\.[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?)*|\[(?:IPv6:[a-f0-9:]+|(?:25[0-5]|2[0-4][0-9]|1[0-9]{2}|[1-9]?[0-9])(?:\.(?:25[0-5]|2[0-4][0-9]|1[0-9]{2}|[1-9]?[0-9])){3})\])$`),
	"hostname":                  isHostname,
	"ipv4":                      lazy(`^(?:` + ipv4Octet + `\.){3}` + ipv4Octet + `$`),
	"ipv6":                      lazy(`(?i)^(?:(?:(?:[0-9a-f]{1,4}:){6}|::(?:[0-9a-f]{1,4}:){5}|(?:[0-9a-f]{1,4})?::(?:[0-9a-f]{1,4}:){4}|(?:(?:[0-9a-f]{1,4}:)?[0-9a-f]{1,4})?::(?:[0-9a-f]{1,4}:){3}|(?:(?:[0-9a-f]{1,4}:){0,2}[0-9a-f]{1,4})?::(?:[0-9a-f]{1,4}:){2}|(?:(?:[0-9a-f]{1,4}:){0,3}[0-9a-f]{1,4})?::[0-9a-f]{1,4}:|(?:(?:[0-9a-f]{1,4}:){0,4}[0-9a-f]{1,4})?::)(?:[0-9a-f]{1,4}:[0-9a-f]{1,4}|(?:` + ipv4Octet + `\.){3}` + ipv4Octet + `)|(?:(?:[0-9a-f]{1,4}:){0,5}[0-9a-f]{1,4})?::[0-9a-f]{1,4}|(?:(?:[0-9a-f]{1,4}:){0,6}[0-9a-f]{1,4})?::)$`),
	"iri":                       isIRI,
	"iri-reference":             isIRIReference,
	"json-pointer":              lazy(`^(?:/(?:[^~/]|~0|~1)*)*$`),
	"json-pointer-uri-fragment": lazy(`(?i)^#(?:/(?:[a-z0-9_\-.!$&'()*+,;:=@]|%[0-9a-f]{2}|~0|~1)*)*$`),
	"regex":                     isRegex,
	"relative-json-pointer":     lazy(`^(?:0|[1-9][0-9]*)(?:#|(?:/(?:[^~/]|~0|~1)*)*)$`),
	"time":                      isTime,
	"uri":                       lazy(`(?i)` + uriPattern),
	"uri-reference":             lazy(`(?i)` + uriReferencePattern),
	"uri-template":              lazy(`(?i)^(?:(?:[^\x00-\x20"<>%\\^` + "`" + `{|}\x7f]|%[0-9a-f]{2})|\{[+#./;?&=,!@|]?(?:[a-z0-9_]|%[0-9a-f]{2})+(?:\.(?:[a-z0-9_]|%[0-9a-f]{2})+)*(?::[1-9]\d{0,3}|\*)?(?:,(?:[a-z0-9_]|%[0-9a-f]{2})+(?:\.(?:[a-z0-9_]|%[0-9a-f]{2})+)*(?::[1-9]\d{0,3}|\*)?)*\})*$`),
	"url":                       func(value string) bool { return urlParses(value, false) },
	"uuid":                      lazy(`(?i)^[0-9a-f]{8}-(?:[0-9a-f]{4}-){3}[0-9a-f]{12}$`),
}

// lazy compiles a pattern when first matched: most processes check no format, and compiling
// them all cost every process milliseconds and over a megabyte as it started.
func lazy(pattern string) func(string) bool {
	compiled := lazyregexp.New(pattern)
	return func(value string) bool { return compiled().MatchString(value) }
}

const ipv4Octet = `(?:25[0-5]|2[0-4]\d|1\d\d|[1-9]?\d)`

// TypeBox's uri and uri-reference patterns, as written.
const (
	uriPattern          = `^[a-z][a-z0-9+\-.]*:(?:\/\/(?:(?:[-a-z0-9._~!$&'()*+,;=:]|%[0-9a-f]{2})*@)?(?:\[(?:(?:(?:[\da-f]{1,4}:){6}(?:[\da-f]{1,4}:[\da-f]{1,4}|(?:(?:25[0-5]|2[0-4]\d|1\d\d|[1-9]?\d)\.){3}(?:25[0-5]|2[0-4]\d|1\d\d|[1-9]?\d))|::(?:[\da-f]{1,4}:){5}(?:[\da-f]{1,4}:[\da-f]{1,4}|(?:(?:25[0-5]|2[0-4]\d|1\d\d|[1-9]?\d)\.){3}(?:25[0-5]|2[0-4]\d|1\d\d|[1-9]?\d))|(?:[\da-f]{1,4})?::(?:[\da-f]{1,4}:){4}(?:[\da-f]{1,4}:[\da-f]{1,4}|(?:(?:25[0-5]|2[0-4]\d|1\d\d|[1-9]?\d)\.){3}(?:25[0-5]|2[0-4]\d|1\d\d|[1-9]?\d))|(?:(?:[\da-f]{1,4}:){0,1}[\da-f]{1,4})?::(?:[\da-f]{1,4}:){3}(?:[\da-f]{1,4}:[\da-f]{1,4}|(?:(?:25[0-5]|2[0-4]\d|1\d\d|[1-9]?\d)\.){3}(?:25[0-5]|2[0-4]\d|1\d\d|[1-9]?\d))|(?:(?:[\da-f]{1,4}:){0,2}[\da-f]{1,4})?::(?:[\da-f]{1,4}:){2}(?:[\da-f]{1,4}:[\da-f]{1,4}|(?:(?:25[0-5]|2[0-4]\d|1\d\d|[1-9]?\d)\.){3}(?:25[0-5]|2[0-4]\d|1\d\d|[1-9]?\d))|(?:(?:[\da-f]{1,4}:){0,3}[\da-f]{1,4})?::[\da-f]{1,4}:(?:[\da-f]{1,4}:[\da-f]{1,4}|(?:(?:25[0-5]|2[0-4]\d|1\d\d|[1-9]?\d)\.){3}(?:25[0-5]|2[0-4]\d|1\d\d|[1-9]?\d))|(?:(?:[\da-f]{1,4}:){0,4}[\da-f]{1,4})?::(?:[\da-f]{1,4}:[\da-f]{1,4}|(?:(?:25[0-5]|2[0-4]\d|1\d\d|[1-9]?\d)\.){3}(?:25[0-5]|2[0-4]\d|1\d\d|[1-9]?\d))|(?:(?:[\da-f]{1,4}:){0,5}[\da-f]{1,4})?::[\da-f]{1,4}|(?:(?:[\da-f]{1,4}:){0,6}[\da-f]{1,4})?::)|v[0-9a-f]+\.[-a-z0-9._~!$&'()*+,;=:]+)\]|(?:(?:25[0-5]|2[0-4]\d|1\d\d|[1-9]?\d)\.){3}(?:25[0-5]|2[0-4]\d|1\d\d|[1-9]?\d)|(?:[-a-z0-9._~!$&'()*+,;=]|%[0-9a-f]{2})*)(?::\d*)?(?:\/(?:[-a-z0-9._~!$&'()*+,;=:@]|%[0-9a-f]{2})*)*|\/(?:(?:[-a-z0-9._~!$&'()*+,;=:@]|%[0-9a-f]{2})+(?:\/(?:[-a-z0-9._~!$&'()*+,;=:@]|%[0-9a-f]{2})*)*)?|(?:[-a-z0-9._~!$&'()*+,;=:@]|%[0-9a-f]{2})+(?:\/(?:[-a-z0-9._~!$&'()*+,;=:@]|%[0-9a-f]{2})*)*)?(?:\?(?:[-a-z0-9._~!$&'()*+,;=:@/?]|%[0-9a-f]{2})*)?(?:#(?:[-a-z0-9._~!$&'()*+,;=:@/?]|%[0-9a-f]{2})*)?$`
	uriReferencePattern = `^(?:[a-z][a-z0-9+\-.]*:(?:\/\/(?:(?:[-a-z0-9._~!$&'()*+,;=:]|%[0-9a-f]{2})*@)?(?:\[(?:(?:(?:[\da-f]{1,4}:){6}(?:[\da-f]{1,4}:[\da-f]{1,4}|(?:(?:25[0-5]|2[0-4]\d|1\d\d|[1-9]?\d)\.){3}(?:25[0-5]|2[0-4]\d|1\d\d|[1-9]?\d))|::(?:[\da-f]{1,4}:){5}(?:[\da-f]{1,4}:[\da-f]{1,4}|(?:(?:25[0-5]|2[0-4]\d|1\d\d|[1-9]?\d)\.){3}(?:25[0-5]|2[0-4]\d|1\d\d|[1-9]?\d))|(?:[\da-f]{1,4})?::(?:[\da-f]{1,4}:){4}(?:[\da-f]{1,4}:[\da-f]{1,4}|(?:(?:25[0-5]|2[0-4]\d|1\d\d|[1-9]?\d)\.){3}(?:25[0-5]|2[0-4]\d|1\d\d|[1-9]?\d))|(?:(?:[\da-f]{1,4}:){0,1}[\da-f]{1,4})?::(?:[\da-f]{1,4}:){3}(?:[\da-f]{1,4}:[\da-f]{1,4}|(?:(?:25[0-5]|2[0-4]\d|1\d\d|[1-9]?\d)\.){3}(?:25[0-5]|2[0-4]\d|1\d\d|[1-9]?\d))|(?:(?:[\da-f]{1,4}:){0,2}[\da-f]{1,4})?::(?:[\da-f]{1,4}:){2}(?:[\da-f]{1,4}:[\da-f]{1,4}|(?:(?:25[0-5]|2[0-4]\d|1\d\d|[1-9]?\d)\.){3}(?:25[0-5]|2[0-4]\d|1\d\d|[1-9]?\d))|(?:(?:[\da-f]{1,4}:){0,3}[\da-f]{1,4})?::[\da-f]{1,4}:(?:[\da-f]{1,4}:[\da-f]{1,4}|(?:(?:25[0-5]|2[0-4]\d|1\d\d|[1-9]?\d)\.){3}(?:25[0-5]|2[0-4]\d|1\d\d|[1-9]?\d))|(?:(?:[\da-f]{1,4}:){0,4}[\da-f]{1,4})?::(?:[\da-f]{1,4}:[\da-f]{1,4}|(?:(?:25[0-5]|2[0-4]\d|1\d\d|[1-9]?\d)\.){3}(?:25[0-5]|2[0-4]\d|1\d\d|[1-9]?\d))|(?:(?:[\da-f]{1,4}:){0,5}[\da-f]{1,4})?::[\da-f]{1,4}|(?:(?:[\da-f]{1,4}:){0,6}[\da-f]{1,4})?::)|v[0-9a-f]+\.[-a-z0-9._~!$&'()*+,;=:]+)\]|(?:(?:25[0-5]|2[0-4]\d|1\d\d|[1-9]?\d)\.){3}(?:25[0-5]|2[0-4]\d|1\d\d|[1-9]?\d)|(?:[-a-z0-9._~!$&'()*+,;=]|%[0-9a-f]{2})*)(?::\d*)?(?:\/(?:[-a-z0-9._~!$&'()*+,;=:@]|%[0-9a-f]{2})*)*|\/(?:(?:[-a-z0-9._~!$&'()*+,;=:@]|%[0-9a-f]{2})+(?:\/(?:[-a-z0-9._~!$&'()*+,;=:@]|%[0-9a-f]{2})*)*)?|(?:[-a-z0-9._~!$&'()*+,;=:@]|%[0-9a-f]{2})+(?:\/(?:[-a-z0-9._~!$&'()*+,;=:@]|%[0-9a-f]{2})*)*)?|(?:\/\/(?:(?:[-a-z0-9._~!$&'()*+,;=:]|%[0-9a-f]{2})*@)?(?:\[(?:(?:(?:[\da-f]{1,4}:){6}(?:[\da-f]{1,4}:[\da-f]{1,4}|(?:(?:25[0-5]|2[0-4]\d|1\d\d|[1-9]?\d)\.){3}(?:25[0-5]|2[0-4]\d|1\d\d|[1-9]?\d))|::(?:[\da-f]{1,4}:){5}(?:[\da-f]{1,4}:[\da-f]{1,4}|(?:(?:25[0-5]|2[0-4]\d|1\d\d|[1-9]?\d)\.){3}(?:25[0-5]|2[0-4]\d|1\d\d|[1-9]?\d))|(?:[\da-f]{1,4})?::(?:[\da-f]{1,4}:){4}(?:[\da-f]{1,4}:[\da-f]{1,4}|(?:(?:25[0-5]|2[0-4]\d|1\d\d|[1-9]?\d)\.){3}(?:25[0-5]|2[0-4]\d|1\d\d|[1-9]?\d))|(?:(?:[\da-f]{1,4}:){0,1}[\da-f]{1,4})?::(?:[\da-f]{1,4}:){3}(?:[\da-f]{1,4}:[\da-f]{1,4}|(?:(?:25[0-5]|2[0-4]\d|1\d\d|[1-9]?\d)\.){3}(?:25[0-5]|2[0-4]\d|1\d\d|[1-9]?\d))|(?:(?:[\da-f]{1,4}:){0,2}[\da-f]{1,4})?::(?:[\da-f]{1,4}:){2}(?:[\da-f]{1,4}:[\da-f]{1,4}|(?:(?:25[0-5]|2[0-4]\d|1\d\d|[1-9]?\d)\.){3}(?:25[0-5]|2[0-4]\d|1\d\d|[1-9]?\d))|(?:(?:[\da-f]{1,4}:){0,3}[\da-f]{1,4})?::[\da-f]{1,4}:(?:[\da-f]{1,4}:[\da-f]{1,4}|(?:(?:25[0-5]|2[0-4]\d|1\d\d|[1-9]?\d)\.){3}(?:25[0-5]|2[0-4]\d|1\d\d|[1-9]?\d))|(?:(?:[\da-f]{1,4}:){0,4}[\da-f]{1,4})?::(?:[\da-f]{1,4}:[\da-f]{1,4}|(?:(?:25[0-5]|2[0-4]\d|1\d\d|[1-9]?\d)\.){3}(?:25[0-5]|2[0-4]\d|1\d\d|[1-9]?\d))|(?:(?:[\da-f]{1,4}:){0,5}[\da-f]{1,4})?::[\da-f]{1,4}|(?:(?:[\da-f]{1,4}:){0,6}[\da-f]{1,4})?::)|v[0-9a-f]+\.[-a-z0-9._~!$&'()*+,;=:]+)\]|(?:(?:25[0-5]|2[0-4]\d|1\d\d|[1-9]?\d)\.){3}(?:25[0-5]|2[0-4]\d|1\d\d|[1-9]?\d)|(?:[-a-z0-9._~!$&'()*+,;=]|%[0-9a-f]{2})*)(?::\d*)?(?:\/(?:[-a-z0-9._~!$&'()*+,;=:@]|%[0-9a-f]{2})*)*|\/(?:(?:[-a-z0-9._~!$&'()*+,;=:@]|%[0-9a-f]{2})+(?:\/(?:[-a-z0-9._~!$&'()*+,;=:@]|%[0-9a-f]{2})*)*)?|(?:[-a-z0-9._~!$&'()*+,;=@]|%[0-9a-f]{2})+(?:\/(?:[-a-z0-9._~!$&'()*+,;=:@]|%[0-9a-f]{2})*)*)?)(?:\?(?:[-a-z0-9._~!$&'()*+,;=:@/?]|%[0-9a-f]{2})*)?(?:#(?:[-a-z0-9._~!$&'()*+,;=:@/?]|%[0-9a-f]{2})*)?$`
)

var datePattern = lazyregexp.New(`^(\d\d\d\d)-(\d\d)-(\d\d)$`)

func isDate(value string) bool {
	match := datePattern().FindStringSubmatch(value)
	if match == nil {
		return false
	}
	year, _ := strconv.Atoi(match[1])
	month, _ := strconv.Atoi(match[2])
	day, _ := strconv.Atoi(match[3])
	days := []int{0, 31, 28, 31, 30, 31, 30, 31, 31, 30, 31, 30, 31}
	if month == 2 && year%4 == 0 && (year%100 != 0 || year%400 == 0) {
		days[2] = 29
	}
	return month >= 1 && month <= 12 && day >= 1 && day <= days[month]
}

var timePattern = lazyregexp.New(`^(\d\d):(\d\d):(\d\d)(?:\.\d+)?(?:([Zz])|([+-])(\d\d):(\d\d))?$`)

// isTime requires an offset, and a leap second only at 23:59:60 UTC.
func isTime(value string) bool {
	match := timePattern().FindStringSubmatch(value)
	if match == nil || match[4] == "" && match[5] == "" {
		return false
	}
	hour, _ := strconv.Atoi(match[1])
	minute, _ := strconv.Atoi(match[2])
	second, _ := strconv.Atoi(match[3])
	offsetHour, _ := strconv.Atoi(match[6])
	offsetMinute, _ := strconv.Atoi(match[7])
	if hour > 23 || minute > 59 || second > 60 || offsetHour > 23 || offsetMinute > 59 {
		return false
	}
	if second < 60 {
		return true
	}
	sign := 1
	if match[5] == "-" {
		sign = -1
	}
	utcMinutes := hour*60 + minute - sign*(offsetHour*60+offsetMinute)
	return (utcMinutes%1440+1440)%1440 == 1439
}

func isDateTime(value string) bool {
	separator := strings.IndexAny(value, "Tt")
	return separator >= 0 && !strings.ContainsAny(value[separator+1:], "Tt") && isDate(value[:separator]) && isTime(value[separator+1:])
}

var hostnameLabel = lazyregexp.New(`^[a-zA-Z0-9-]*$`)

// isHostname checks LDH labels. An xn-- label is accepted when it is LDH:
// pi-schema decodes it and checks the Unicode label, which needs IDNA tables.
func isHostname(value string) bool {
	if value == "" || len(value) > 253 || strings.HasSuffix(value, ".") {
		return false
	}
	for _, label := range strings.Split(value, ".") {
		if label == "" || len(label) > 63 || !hostnameLabel().MatchString(label) {
			return false
		}
		if strings.HasPrefix(strings.ToLower(label), "xn--") {
			continue
		}
		if strings.HasPrefix(label, "-") || strings.HasSuffix(label, "-") || len(label) >= 4 && label[2:4] == "--" {
			return false
		}
	}
	return true
}

// isRegex accepts what Go's RE2 compiles, and the ECMAScript lookarounds it
// refuses.
func isRegex(value string) bool {
	_, err := regexp.Compile(value)
	var syntaxError *syntax.Error
	return err == nil || errors.As(err, &syntaxError) && syntaxError.Code == syntax.ErrInvalidPerlOp
}

func isIRI(value string) bool {
	if strings.ContainsAny(value, "<>^`{|}\\") || strings.ContainsFunc(value, func(r rune) bool { return r <= 0x20 }) || !validPercents(value) {
		return false
	}
	return urlParses(value, false)
}

var malformedScheme = lazyregexp.New(`^[a-zA-Z][a-zA-Z0-9+\-.]*//`)

func isIRIReference(value string) bool {
	if strings.ContainsAny(value, "\\\x7f") || strings.ContainsFunc(value, func(r rune) bool { return r <= 0x20 }) || !validPercents(value) || malformedScheme().MatchString(value) {
		return false
	}
	return urlParses(value, true)
}

func validPercents(value string) bool {
	for index := 0; index < len(value); index++ {
		if value[index] == '%' && (index+2 >= len(value) || !isHex(value[index+1]) || !isHex(value[index+2])) {
			return false
		}
	}
	return true
}

func isHex(char byte) bool {
	return char >= '0' && char <= '9' || char >= 'a' && char <= 'f' || char >= 'A' && char <= 'F'
}

var urlScheme = lazyregexp.New(`^[a-zA-Z][a-zA-Z0-9+\-.]*:`)

// urlParses approximates WHATWG URL.canParse(value), or with relative set
// URL.canParse(value, "http://example.com"): a scheme is required unless
// relative, and a special scheme or an authority needs a valid host.
func urlParses(value string, relative bool) bool {
	value = strings.TrimFunc(value, func(r rune) bool { return r <= 0x20 })
	value = strings.NewReplacer("\t", "", "\n", "", "\r", "").Replace(value)
	scheme := urlScheme().FindString(value)
	if scheme == "" {
		if !relative {
			return false
		}
		rest := strings.ReplaceAll(value, "\\", "/")
		if !strings.HasPrefix(rest, "//") {
			return true
		}
		return validHost(strings.TrimPrefix(rest, "//"), true)
	}
	rest := value[len(scheme):]
	switch strings.ToLower(scheme) {
	case "file:":
		return true
	case "http:", "https:", "ws:", "wss:", "ftp:":
		return validHost(strings.TrimLeft(rest, "/\\"), true)
	}
	if !strings.HasPrefix(rest, "//") {
		return true
	}
	return validHost(rest[2:], false)
}

// validHost checks the host of the authority that starts authority. A
// special scheme's host is percent-decoded, must not be empty and refuses %.
func validHost(authority string, special bool) bool {
	terminators := "/?#"
	if special {
		terminators += "\\"
	}
	host := authority
	if end := strings.IndexAny(authority, terminators); end >= 0 {
		host = authority[:end]
	}
	if at := strings.LastIndexByte(host, '@'); at >= 0 {
		host = host[at+1:]
	}
	if strings.HasPrefix(host, "[") {
		closing := strings.IndexByte(host, ']')
		if closing < 0 || net.ParseIP(host[1:closing]) == nil || !strings.Contains(host[1:closing], ":") {
			return false
		}
		port := host[closing+1:]
		return port == "" || strings.HasPrefix(port, ":") && validPort(port[1:])
	}
	if colon := strings.LastIndexByte(host, ':'); colon >= 0 {
		if !validPort(host[colon+1:]) {
			return false
		}
		host = host[:colon]
	}
	forbidden := " #/:<>?@[\\]^|"
	if special {
		decoded, err := url.PathUnescape(host)
		if err != nil || decoded == "" {
			return false
		}
		host, forbidden = decoded, forbidden+"%"
	}
	return !strings.ContainsAny(host, forbidden) && !strings.ContainsFunc(host, func(r rune) bool { return r < 0x20 || r == 0x7f })
}

func validPort(port string) bool {
	if port == "" {
		return true
	}
	number, err := strconv.Atoi(port)
	return err == nil && number <= 65535 && strings.Trim(port, "0123456789") == ""
}
