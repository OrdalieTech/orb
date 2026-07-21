// Package semver implements the subset of npm's semver used by pi package
// management: exact versions, ranges (^ ~ x-ranges, hyphen, ||, primitives),
// Satisfies and MaxSatisfying with npm's prerelease-inclusion rule.
package semver

import (
	"strconv"
	"strings"
)

type Version struct {
	Major, Minor, Patch int
	Prerelease          []string
}

// Parse accepts an exact npm version ("1.2.3", "v1.2.3", "1.2.3-rc.1+build").
func Parse(input string) (Version, bool) {
	raw := strings.TrimSpace(input)
	s := strings.TrimPrefix(strings.TrimPrefix(raw, "v"), "=")
	if build := strings.IndexByte(s, '+'); build >= 0 {
		s = s[:build]
	}
	prerelease := []string(nil)
	if dash := strings.IndexByte(s, '-'); dash >= 0 {
		pre := s[dash+1:]
		s = s[:dash]
		if pre == "" {
			return Version{}, false
		}
		prerelease = strings.Split(pre, ".")
		for _, id := range prerelease {
			if !validIdentifier(id) {
				return Version{}, false
			}
		}
	}
	parts := strings.Split(s, ".")
	if len(parts) != 3 {
		return Version{}, false
	}
	numbers := make([]int, 3)
	for index, part := range parts {
		value, ok := parseNumericIdentifier(part)
		if !ok {
			return Version{}, false
		}
		numbers[index] = value
	}
	return Version{Major: numbers[0], Minor: numbers[1], Patch: numbers[2], Prerelease: prerelease}, true
}

func validIdentifier(id string) bool {
	if id == "" {
		return false
	}
	for _, r := range id {
		if r != '-' && (r < '0' || r > '9') && (r < 'a' || r > 'z') && (r < 'A' || r > 'Z') {
			return false
		}
	}
	return true
}

func parseNumericIdentifier(s string) (int, bool) {
	if s == "" || (len(s) > 1 && s[0] == '0') {
		return 0, false
	}
	value, err := strconv.Atoi(s)
	if err != nil || value < 0 {
		return 0, false
	}
	return value, true
}

// Valid reports whether input is an exact version (npm semver.valid).
func Valid(input string) bool {
	_, ok := Parse(input)
	return ok
}

// Compare returns -1, 0, or 1 by npm semver precedence (build ignored).
func Compare(a, b Version) int {
	if c := compareInt(a.Major, b.Major); c != 0 {
		return c
	}
	if c := compareInt(a.Minor, b.Minor); c != 0 {
		return c
	}
	if c := compareInt(a.Patch, b.Patch); c != 0 {
		return c
	}
	return comparePrerelease(a.Prerelease, b.Prerelease)
}

func compareInt(a, b int) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	default:
		return 0
	}
}

func comparePrerelease(a, b []string) int {
	if len(a) == 0 && len(b) == 0 {
		return 0
	}
	if len(a) == 0 {
		return 1
	}
	if len(b) == 0 {
		return -1
	}
	for index := 0; ; index++ {
		if index >= len(a) && index >= len(b) {
			return 0
		}
		if index >= len(a) {
			return -1
		}
		if index >= len(b) {
			return 1
		}
		left, right := a[index], b[index]
		leftNumber, leftNumeric := parseLooseNumber(left)
		rightNumber, rightNumeric := parseLooseNumber(right)
		switch {
		case leftNumeric && rightNumeric:
			if c := compareInt(leftNumber, rightNumber); c != 0 {
				return c
			}
		case leftNumeric:
			return -1
		case rightNumeric:
			return 1
		default:
			if left != right {
				if left < right {
					return -1
				}
				return 1
			}
		}
	}
}

func parseLooseNumber(s string) (int, bool) {
	value, err := strconv.Atoi(s)
	if err != nil || value < 0 {
		return 0, false
	}
	return value, true
}

type comparator struct {
	op      string // "<", "<=", ">", ">=", "=", or "" for match-any
	version Version
	any     bool
}

func (c comparator) matches(v Version) bool {
	if c.any {
		return true
	}
	cmp := Compare(v, c.version)
	switch c.op {
	case "<":
		return cmp < 0
	case "<=":
		return cmp <= 0
	case ">":
		return cmp > 0
	case ">=":
		return cmp >= 0
	default:
		return cmp == 0
	}
}

type comparatorSet []comparator

// Range is a parsed npm range: OR of comparator sets.
type Range struct {
	sets []comparatorSet
}

// ValidRange reports whether input parses as an npm range.
func ValidRange(input string) bool {
	_, ok := ParseRange(input)
	return ok
}

func ParseRange(input string) (Range, bool) {
	raw := strings.TrimSpace(input)
	sets := make([]comparatorSet, 0, 1)
	for part := range strings.SplitSeq(raw, "||") {
		set, ok := parseComparatorSet(strings.TrimSpace(part))
		if !ok {
			return Range{}, false
		}
		sets = append(sets, set)
	}
	return Range{sets: sets}, true
}

func parseComparatorSet(input string) (comparatorSet, bool) {
	if input == "" {
		return comparatorSet{{any: true}}, true
	}
	if before, after, found := strings.Cut(input, " - "); found {
		return parseHyphen(strings.TrimSpace(before), strings.TrimSpace(after))
	}
	set := comparatorSet{}
	for _, token := range strings.Fields(input) {
		comparators, ok := parseSimple(token)
		if !ok {
			return nil, false
		}
		set = append(set, comparators...)
	}
	if len(set) == 0 {
		set = append(set, comparator{any: true})
	}
	return set, true
}

type partial struct {
	major, minor, patch int
	hasMinor, hasPatch  bool
	prerelease          []string
	anyMajor            bool
}

func parsePartial(input string) (partial, bool) {
	s := strings.TrimPrefix(strings.TrimPrefix(input, "v"), "=")
	if s == "" || s == "*" || strings.EqualFold(s, "x") {
		return partial{anyMajor: true}, true
	}
	if build := strings.IndexByte(s, '+'); build >= 0 {
		s = s[:build]
	}
	var prerelease []string
	if dash := strings.IndexByte(s, '-'); dash >= 0 {
		pre := s[dash+1:]
		s = s[:dash]
		if pre == "" {
			return partial{}, false
		}
		prerelease = strings.Split(pre, ".")
	}
	parts := strings.Split(s, ".")
	if len(parts) > 3 {
		return partial{}, false
	}
	result := partial{}
	for index, part := range parts {
		if strings.EqualFold(part, "x") || part == "*" {
			// x truncates everything after it.
			if index == 0 {
				return partial{anyMajor: true}, true
			}
			break
		}
		value, ok := parseNumericIdentifier(part)
		if !ok {
			return partial{}, false
		}
		switch index {
		case 0:
			result.major = value
		case 1:
			result.minor = value
			result.hasMinor = true
		case 2:
			result.patch = value
			result.hasPatch = true
		}
	}
	if result.hasPatch {
		result.prerelease = prerelease
	} else if prerelease != nil {
		return partial{}, false
	}
	return result, true
}

func (p partial) exact() Version {
	return Version{Major: p.major, Minor: p.minor, Patch: p.patch, Prerelease: p.prerelease}
}

func (p partial) full() bool { return !p.anyMajor && p.hasMinor && p.hasPatch }

// nextBoundary returns the "<X.Y.0-0" style upper bound version.
func lowerBound(p partial) comparator {
	return comparator{op: ">=", version: p.exact()}
}

func upperBoundExclusive(major, minor int) comparator {
	return comparator{op: "<", version: Version{Major: major, Minor: minor, Prerelease: []string{"0"}}}
}

func parseSimple(token string) ([]comparator, bool) {
	switch {
	case strings.HasPrefix(token, "^"):
		p, ok := parsePartial(token[1:])
		if !ok {
			return nil, false
		}
		if p.anyMajor {
			return []comparator{{any: true}}, true
		}
		lower := lowerBound(p)
		switch {
		case !p.hasMinor:
			return []comparator{lower, upperBoundExclusive(p.major+1, 0)}, true
		case !p.hasPatch:
			if p.major == 0 {
				return []comparator{lower, upperBoundExclusive(0, p.minor+1)}, true
			}
			return []comparator{lower, upperBoundExclusive(p.major+1, 0)}, true
		case p.major == 0 && p.minor == 0:
			return []comparator{lower, {op: "<", version: Version{Major: 0, Minor: 0, Patch: p.patch + 1, Prerelease: []string{"0"}}}}, true
		case p.major == 0:
			return []comparator{lower, upperBoundExclusive(0, p.minor+1)}, true
		default:
			return []comparator{lower, upperBoundExclusive(p.major+1, 0)}, true
		}
	case strings.HasPrefix(token, "~"):
		body := strings.TrimPrefix(token[1:], ">") // "~>" is accepted by npm as "~"
		p, ok := parsePartial(body)
		if !ok {
			return nil, false
		}
		if p.anyMajor {
			return []comparator{{any: true}}, true
		}
		if !p.hasMinor {
			return []comparator{lowerBound(p), upperBoundExclusive(p.major+1, 0)}, true
		}
		return []comparator{lowerBound(p), upperBoundExclusive(p.major, p.minor+1)}, true
	case strings.HasPrefix(token, ">="), strings.HasPrefix(token, "<="):
		op := token[:2]
		p, ok := parsePartial(token[2:])
		if !ok {
			return nil, false
		}
		return primitiveComparators(op, p), true
	case strings.HasPrefix(token, ">"), strings.HasPrefix(token, "<"):
		op := token[:1]
		p, ok := parsePartial(token[1:])
		if !ok {
			return nil, false
		}
		return primitiveComparators(op, p), true
	default:
		p, ok := parsePartial(token)
		if !ok {
			return nil, false
		}
		return primitiveComparators("=", p), true
	}
}

// primitiveComparators desugars x-partials per node-semver's replaceXRange.
func primitiveComparators(op string, p partial) []comparator {
	if p.anyMajor {
		if op == "<" {
			return []comparator{{op: "<", version: Version{Prerelease: []string{"0"}}}}
		}
		return []comparator{{any: true}}
	}
	if p.full() {
		return []comparator{{op: op, version: p.exact()}}
	}
	switch op {
	case ">":
		if !p.hasMinor {
			return []comparator{{op: ">=", version: Version{Major: p.major + 1, Prerelease: []string{"0"}}}}
		}
		return []comparator{{op: ">=", version: Version{Major: p.major, Minor: p.minor + 1, Prerelease: []string{"0"}}}}
	case "<":
		return []comparator{{op: "<", version: Version{Major: p.major, Minor: p.minor, Prerelease: []string{"0"}}}}
	case ">=":
		return []comparator{{op: ">=", version: p.exact()}}
	case "<=":
		if !p.hasMinor {
			return []comparator{upperBoundExclusive(p.major+1, 0)}
		}
		return []comparator{upperBoundExclusive(p.major, p.minor+1)}
	default: // "=" with x parts expands to a range
		lower := comparator{op: ">=", version: p.exact()}
		if !p.hasMinor {
			return []comparator{lower, upperBoundExclusive(p.major+1, 0)}
		}
		return []comparator{lower, upperBoundExclusive(p.major, p.minor+1)}
	}
}

func parseHyphen(low, high string) (comparatorSet, bool) {
	lowPartial, ok := parsePartial(low)
	if !ok {
		return nil, false
	}
	highPartial, ok := parsePartial(high)
	if !ok {
		return nil, false
	}
	set := comparatorSet{}
	if !lowPartial.anyMajor {
		set = append(set, comparator{op: ">=", version: lowPartial.exact()})
	}
	switch {
	case highPartial.anyMajor:
	case !highPartial.hasMinor:
		set = append(set, upperBoundExclusive(highPartial.major+1, 0))
	case !highPartial.hasPatch:
		set = append(set, upperBoundExclusive(highPartial.major, highPartial.minor+1))
	default:
		set = append(set, comparator{op: "<=", version: highPartial.exact()})
	}
	if len(set) == 0 {
		set = append(set, comparator{any: true})
	}
	return set, true
}

// Satisfies applies npm's rule that prerelease versions only match ranges
// whose comparator set names a prerelease on the same [major,minor,patch].
func Satisfies(version string, rangeInput string) bool {
	v, ok := Parse(version)
	if !ok {
		return false
	}
	r, ok := ParseRange(rangeInput)
	if !ok {
		return false
	}
	return r.Matches(v)
}

func (r Range) Matches(v Version) bool {
	for _, set := range r.sets {
		if setMatches(set, v) {
			return true
		}
	}
	return false
}

func setMatches(set comparatorSet, v Version) bool {
	for _, c := range set {
		if !c.matches(v) {
			return false
		}
	}
	if len(v.Prerelease) == 0 {
		return true
	}
	for _, c := range set {
		if c.any {
			continue
		}
		if len(c.version.Prerelease) > 0 && c.version.Major == v.Major &&
			c.version.Minor == v.Minor && c.version.Patch == v.Patch {
			return true
		}
	}
	return false
}

// MaxSatisfying returns the highest version matching the range, or "".
func MaxSatisfying(versions []string, rangeInput string) string {
	r, ok := ParseRange(rangeInput)
	if !ok {
		return ""
	}
	best := ""
	var bestVersion Version
	for _, candidate := range versions {
		v, ok := Parse(candidate)
		if !ok || !r.Matches(v) {
			continue
		}
		if best == "" || Compare(v, bestVersion) > 0 {
			best = candidate
			bestVersion = v
		}
	}
	return best
}
