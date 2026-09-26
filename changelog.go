// Package orb holds what the product ships beside its code.
package orb

import _ "embed"

// Changelog is Orb's own release notes, which /changelog shows.
//
//go:embed CHANGELOG.md
var Changelog string
