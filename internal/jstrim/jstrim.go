// Package jstrim ports the ECMAScript String.prototype.trim whitespace set
// (WhiteSpace + LineTerminator), which differs from Go's unicode.IsSpace.
package jstrim

// IsSpace reports whether character is trimmed by JavaScript's String.trim.
func IsSpace(character rune) bool {
	switch {
	case character >= '\t' && character <= '\r':
		return true
	case character == ' ', character == '\u00a0', character == '\u1680', character == '\u2028', character == '\u2029', character == '\u202f', character == '\u205f', character == '\u3000', character == '\ufeff':
		return true
	case character >= '\u2000' && character <= '\u200a':
		return true
	default:
		return false
	}
}
