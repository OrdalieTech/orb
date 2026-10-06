// Package ptr holds the pointer helpers the clone code in every layer shares.
package ptr

// Clone returns a pointer to a copy of *value; nil stays nil.
func Clone[T any](value *T) *T {
	if value == nil {
		return nil
	}
	return new(*value)
}
