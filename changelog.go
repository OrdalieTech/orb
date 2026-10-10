// Package orb holds what the product ships beside its code.
package orb

import _ "embed"

// Changelog is Orb's own release notes, which /changelog shows.
//
//go:embed CHANGELOG.md
var Changelog string

// Skill is the agent skill describing Orb: `orb skill` prints it for other
// agents, and the CLI lists it to its own.
//
//go:embed SKILL.md
var Skill string
