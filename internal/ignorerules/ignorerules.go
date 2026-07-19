// Package ignorerules holds the shared gitignore-rule matching loop that both
// skill loaders (agent/harness and codingagent) port from the npm `ignore`
// dependency. Rule parsing stays in each caller; only the match loop is shared.
package ignorerules

import (
	"strings"

	"github.com/bmatcuk/doublestar/v4"
)

// Rule is one parsed gitignore-style rule, scoped to the slash-separated Base
// directory it was declared in.
type Rule struct {
	Base         string
	Pattern      string
	Negated      bool
	Directory    bool
	BasenameOnly bool
}

// Ignores reports whether the slash-separated relativePath is ignored by the
// rules, mirroring the npm `ignore` library loop (last matching rule wins).
func Ignores(rules []Rule, relativePath string, directory bool) bool {
	relativePath = strings.TrimSuffix(relativePath, "/")
	ignored := false
	for _, rule := range rules {
		local := relativePath
		if rule.Base != "" {
			if local == rule.Base {
				local = ""
			} else if strings.HasPrefix(local, rule.Base+"/") {
				local = strings.TrimPrefix(local, rule.Base+"/")
			} else {
				continue
			}
		}
		if local == "" {
			continue
		}
		matched := false
		if rule.BasenameOnly {
			parts := strings.Split(local, "/")
			for index, part := range parts {
				if ok, _ := doublestar.Match(rule.Pattern, part); ok && (!rule.Directory || directory || index < len(parts)-1) {
					matched = true
					break
				}
			}
		} else {
			matched, _ = doublestar.Match(rule.Pattern, local)
			if !matched {
				matched, _ = doublestar.Match(strings.TrimSuffix(rule.Pattern, "/")+"/**", local)
			}
		}
		if matched {
			ignored = !rule.Negated
		}
	}
	return ignored
}
