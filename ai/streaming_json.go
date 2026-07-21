package ai

import "github.com/OrdalieTech/pigo/internal/partialjson"

// ParseStreamingJSON parses potentially incomplete JSON captured while a tool
// call streams. It never fails: complete input parses strictly (retrying with
// string repair), partial input yields the values received so far, and empty
// or unusable input becomes an empty object — matching parseStreamingJson
// exported from upstream pi-ai's index for custom stream handlers.
func ParseStreamingJSON(input string) any {
	return partialjson.ParseStreamingJSON(input)
}
