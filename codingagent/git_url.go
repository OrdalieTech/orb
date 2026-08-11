package codingagent

import (
	neturl "net/url"
	"regexp"
	"strings"
)

// Port of packages/coding-agent/src/utils/git.ts. The upstream hosted-git-info
// dependency is replaced by knownGitHostParse, which reproduces its behavior
// for the hosts it recognizes; unknown hosts fall through to the generic
// parser exactly as upstream does.

// GitSource is a parsed git package source.
type GitSource struct {
	Repo   string `json:"repo"`
	Host   string `json:"host"`
	Path   string `json:"path"`
	Ref    string `json:"ref,omitempty"`
	Pinned bool   `json:"pinned"`
}

var scpLikeRe = regexp.MustCompile(`^git@([^:]+):(.+)$`)

func splitGitRef(url string) (repo, ref string) {
	if match := scpLikeRe.FindStringSubmatch(url); match != nil {
		pathWithMaybeRef := match[2]
		refSeparator := strings.Index(pathWithMaybeRef, "@")
		if refSeparator < 0 {
			return url, ""
		}
		repoPath := pathWithMaybeRef[:refSeparator]
		splitRef := pathWithMaybeRef[refSeparator+1:]
		if repoPath == "" || splitRef == "" {
			return url, ""
		}
		return "git@" + match[1] + ":" + repoPath, splitRef
	}

	if strings.Contains(url, "://") {
		parsed, err := neturl.Parse(url)
		if err != nil {
			return url, ""
		}
		pathWithMaybeRef := strings.TrimLeft(parsed.EscapedPath(), "/")
		refSeparator := strings.Index(pathWithMaybeRef, "@")
		if refSeparator < 0 {
			return url, ""
		}
		repoPath := pathWithMaybeRef[:refSeparator]
		splitRef := pathWithMaybeRef[refSeparator+1:]
		if repoPath == "" || splitRef == "" {
			return url, ""
		}
		parsed.RawPath = ""
		parsed.Path = "/" + repoPath
		return strings.TrimSuffix(parsed.String(), "/"), splitRef
	}

	slashIndex := strings.Index(url, "/")
	if slashIndex < 0 {
		return url, ""
	}
	host := url[:slashIndex]
	pathWithMaybeRef := url[slashIndex+1:]
	refSeparator := strings.Index(pathWithMaybeRef, "@")
	if refSeparator < 0 {
		return url, ""
	}
	repoPath := pathWithMaybeRef[:refSeparator]
	splitRef := pathWithMaybeRef[refSeparator+1:]
	if repoPath == "" || splitRef == "" {
		return url, ""
	}
	return host + "/" + repoPath, splitRef
}

func hasUnsafeGitInstallPart(value string, allowSlash bool) bool {
	decoded, err := neturl.PathUnescape(value)
	if err != nil {
		return true
	}
	for _, candidate := range []string{value, decoded} {
		if strings.Contains(candidate, "\x00") || strings.Contains(candidate, `\`) || strings.HasPrefix(candidate, "/") {
			return true
		}
		if !allowSlash && strings.Contains(candidate, "/") {
			return true
		}
		for part := range strings.SplitSeq(candidate, "/") {
			if part == ".." {
				return true
			}
		}
	}
	return false
}

var gitSuffixRe = regexp.MustCompile(`\.git$`)

func buildGitSource(repo, host, path, ref string) *GitSource {
	if strings.HasPrefix(path, "/") {
		return nil
	}
	normalizedPath := strings.TrimLeft(gitSuffixRe.ReplaceAllString(path, ""), "/")
	if host == "" || normalizedPath == "" || len(strings.Split(normalizedPath, "/")) < 2 {
		return nil
	}
	if hasUnsafeGitInstallPart(host, false) || hasUnsafeGitInstallPart(normalizedPath, true) {
		return nil
	}
	return &GitSource{Repo: repo, Host: host, Path: normalizedPath, Ref: ref, Pinned: ref != ""}
}

var protocolRe = regexp.MustCompile(`(?i)^(https?|ssh|git):\/\/`)

var schemeSlashRunRe = regexp.MustCompile(`^([a-zA-Z][a-zA-Z0-9+.-]*):/+`)

func parseGenericGitURL(url string) *GitSource {
	repoWithoutRef, ref := splitGitRef(url)
	repo := repoWithoutRef
	host := ""
	path := ""

	if match := scpLikeRe.FindStringSubmatch(repoWithoutRef); match != nil {
		host = match[1]
		path = match[2]
	} else if strings.HasPrefix(repoWithoutRef, "https://") || strings.HasPrefix(repoWithoutRef, "http://") ||
		strings.HasPrefix(repoWithoutRef, "ssh://") || strings.HasPrefix(repoWithoutRef, "git://") {
		parsed, err := neturl.Parse(repoWithoutRef)
		if err != nil {
			return nil
		}
		host = parsed.Hostname()
		path = strings.TrimLeft(parsed.EscapedPath(), "/")
	} else {
		slashIndex := strings.Index(repoWithoutRef, "/")
		if slashIndex < 0 {
			return nil
		}
		host = repoWithoutRef[:slashIndex]
		path = repoWithoutRef[slashIndex+1:]
		if !strings.Contains(host, ".") && host != "localhost" {
			return nil
		}
		repo = "https://" + repoWithoutRef
	}

	return buildGitSource(repo, host, path, ref)
}

// knownGitHostInfo mirrors the hosted-git-info fields parseGitURL consumes.
type knownGitHostInfo struct {
	domain     string
	user       string
	project    string
	committish string
}

var knownGitHosts = map[string]struct{}{
	"github.com":    {},
	"gitlab.com":    {},
	"bitbucket.org": {},
	"git.sr.ht":     {},
}

// Shortcut form "user/repo" (hosted-git-info's github default shortcut).
var gitShortcutRe = regexp.MustCompile(`^([^:@%/\s.-][^:@%/\s]*)/([^:@\s/%]+?)(?:\.git)?(#.*)?$`)

func knownGitHostParse(candidate string) *knownGitHostInfo {
	committish := ""
	if hash := strings.Index(candidate, "#"); hash >= 0 {
		committish = candidate[hash+1:]
		candidate = candidate[:hash]
	}

	host := ""
	pathPart := ""
	switch {
	case scpLikeRe.MatchString(candidate):
		match := scpLikeRe.FindStringSubmatch(candidate)
		host = match[1]
		pathPart = match[2]
	case strings.Contains(candidate, "://"):
		normalized := strings.TrimPrefix(candidate, "git+")
		// WHATWG URL collapses any run of slashes after a special scheme
		// ("https:////host" parses like "https://host"), which upstream's
		// hosted-git-info relies on for the git:-prefix-eaten git:// form.
		normalized = schemeSlashRunRe.ReplaceAllString(normalized, "$1://")
		if !protocolRe.MatchString(normalized) {
			return nil
		}
		parsed, err := neturl.Parse(normalized)
		if err != nil {
			return nil
		}
		host = parsed.Hostname()
		pathPart = strings.Trim(parsed.EscapedPath(), "/")
	default:
		match := gitShortcutRe.FindStringSubmatch(candidate + committishSuffix(committish))
		if match == nil {
			return nil
		}
		return &knownGitHostInfo{domain: "github.com", user: match[1], project: match[2], committish: strings.TrimPrefix(match[3], "#")}
	}

	host = strings.ToLower(host)
	host = strings.TrimPrefix(host, "www.")
	if _, known := knownGitHosts[host]; !known {
		return nil
	}

	segments := []string{}
	for segment := range strings.SplitSeq(strings.Trim(pathPart, "/"), "/") {
		if segment != "" {
			segments = append(segments, segment)
		}
	}
	if len(segments) < 2 {
		return nil
	}
	project := gitSuffixRe.ReplaceAllString(segments[len(segments)-1], "")
	switch host {
	case "github.com":
		if len(segments) > 2 {
			// Only /tree/<committish> extra path is recognized, as in
			// hosted-git-info's github extractor.
			if segments[2] != "tree" || len(segments) < 4 {
				return nil
			}
			if committish == "" {
				committish = strings.Join(segments[3:], "/")
			}
			project = gitSuffixRe.ReplaceAllString(segments[1], "")
			return &knownGitHostInfo{domain: host, user: segments[0], project: project, committish: committish}
		}
		return &knownGitHostInfo{domain: host, user: segments[0], project: project, committish: committish}
	case "bitbucket.org", "git.sr.ht":
		if len(segments) != 2 {
			return nil
		}
		return &knownGitHostInfo{domain: host, user: segments[0], project: project, committish: committish}
	default: // gitlab.com supports subgroups
		return &knownGitHostInfo{domain: host, user: strings.Join(segments[:len(segments)-1], "/"), project: project, committish: committish}
	}
}

func committishSuffix(committish string) string {
	if committish == "" {
		return ""
	}
	return "#" + committish
}

func hasGitProtocolPrefix(value string) bool {
	return strings.HasPrefix(value, "http://") || strings.HasPrefix(value, "https://") ||
		strings.HasPrefix(value, "ssh://") || strings.HasPrefix(value, "git://")
}

// ParseGitURL parses a git source.
//
// Rules:
//   - With git: prefix, all historical shorthand forms are accepted.
//   - Without git: prefix, only explicit protocol URLs are accepted.
func ParseGitURL(source string) *GitSource {
	trimmed := strings.TrimSpace(source)
	hasGitPrefix := strings.HasPrefix(trimmed, "git:")
	url := trimmed
	if hasGitPrefix {
		url = strings.TrimSpace(trimmed[4:])
	}

	if !hasGitPrefix && !protocolRe.MatchString(url) {
		return nil
	}

	splitRepo, splitRef := splitGitRef(url)

	hostedCandidates := make([]string, 0, 2)
	if splitRef != "" {
		hostedCandidates = append(hostedCandidates, splitRepo+"#"+splitRef)
	}
	hostedCandidates = append(hostedCandidates, url)
	for _, candidate := range hostedCandidates {
		info := knownGitHostParse(candidate)
		if info == nil {
			continue
		}
		if splitRef != "" && strings.Contains(info.project, "@") {
			continue
		}
		repo := splitRepo
		if !hasGitProtocolPrefix(splitRepo) && !strings.HasPrefix(splitRepo, "git@") {
			repo = "https://" + splitRepo
		}
		ref := info.committish
		if ref == "" {
			ref = splitRef
		}
		return buildGitSource(repo, info.domain, info.user+"/"+info.project, ref)
	}

	httpsCandidates := make([]string, 0, 2)
	if splitRef != "" {
		httpsCandidates = append(httpsCandidates, "https://"+splitRepo+"#"+splitRef)
	}
	httpsCandidates = append(httpsCandidates, "https://"+url)
	for _, candidate := range httpsCandidates {
		info := knownGitHostParse(candidate)
		if info == nil {
			continue
		}
		if splitRef != "" && strings.Contains(info.project, "@") {
			continue
		}
		ref := info.committish
		if ref == "" {
			ref = splitRef
		}
		return buildGitSource("https://"+splitRepo, info.domain, info.user+"/"+info.project, ref)
	}

	return parseGenericGitURL(url)
}
