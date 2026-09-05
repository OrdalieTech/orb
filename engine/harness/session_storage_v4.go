package harness

import (
	"bytes"
	"encoding/json"
	"errors"
)

func isV4HarnessHeaderLine(line []byte) bool {
	var probe struct {
		Kind string `json:"kind"`
	}
	return json.Unmarshal(bytes.TrimSpace(line), &probe) == nil && probe.Kind == "header"
}

// Transaction storage has no model-change or lane-record tree nodes, so the
// historical v3 projection cannot preserve its public tree semantics.
func rehydrateV4JSONLSession(_ []byte, _ string, _ func([]byte) error) (*JSONLSessionStorage, error) {
	return nil, errors.New("harness v4 sessions require the SessionV4TransactionStorage API; the v3 session projection is no longer supported")
}
