package harness

import (
	"bytes"
	"encoding/json"
	"fmt"
	"github.com/OrdalieTech/orb/internal/partialjson"
)

// SessionV4TransactionHeader is the released v0.85 storage header. The earlier
// SessionV4Header remains available for consumers of the pre-transaction format.
type SessionV4TransactionHeader struct {
	V                       int     `json:"v"`
	Kind                    string  `json:"kind"`
	ID                      string  `json:"id"`
	StorageVersion          int64   `json:"storageVersion"`
	CreatedAt               int64   `json:"createdAt"`
	CWD                     string  `json:"cwd"`
	ParentSessionID         *string `json:"parentSessionId,omitempty"`
	LegacyParentSessionPath *string `json:"legacyParentSessionPath,omitempty"`
	NextSeq                 *int64  `json:"nextSeq,omitempty"`
}

// DecodeSessionV4TransactionHeader validates the storage header without imposing
// supported storage-version policy; upstream separates decoding from opening.
//
//nolint:staticcheck // Error capitalization matches the upstream storage protocol.
func DecodeSessionV4TransactionHeader(data []byte) (SessionV4TransactionHeader, error) {
	var header SessionV4TransactionHeader
	_, fields, parseErr := parseV4Object(data)
	if parseErr != nil {
		if !json.Valid(data) {
			return header, fmt.Errorf("Invalid JSONL session header: not valid JSON")
		}
		return header, fmt.Errorf("Unsupported JSONL session header")
	}
	v, vok := v4SafeInteger(fields["v"])
	kind, _ := v4String(fields["kind"])
	_, idOK := v4String(fields["id"])
	_, cwdOK := v4String(fields["cwd"])
	storageVersion, storageOK := transactionInteger(fields["storageVersion"], 1)
	_, createdOK := transactionInteger(fields["createdAt"], 0)
	valid := vok && v == 4 && kind == "header" && idOK && cwdOK && storageOK && createdOK
	for _, field := range []string{"parentSessionId", "legacyParentSessionPath"} {
		if raw, found := fields[field]; found {
			_, ok := v4String(raw)
			valid = valid && ok
		}
	}
	if raw, found := fields["nextSeq"]; found {
		_, ok := transactionInteger(raw, 1)
		valid = valid && ok
	}
	if !valid {
		return header, fmt.Errorf("Unsupported JSONL session header")
	}
	if err := json.Unmarshal(data, &header); err != nil {
		return header, err
	}
	header.StorageVersion = storageVersion
	return header, nil
}

func transactionInteger(raw json.RawMessage, minimum int64) (int64, bool) {
	if len(raw) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return 0, false
	}
	value, ok := v4SafeInteger(raw)
	return value, ok && value >= minimum
}

// SessionV4PreparedTransaction holds storage-assigned writes in upstream field
// order. A durable transaction occupies one JSONL line, including multi-writes.
type SessionV4PreparedTransaction struct {
	Writes []json.RawMessage          `json:"writes"`
	Result SessionV4TransactionResult `json:"result"`
}

type SessionV4TransactionResult struct {
	FirstSeq  int64   `json:"firstSeq"`
	Seqs      []int64 `json:"seqs"`
	Timestamp int64   `json:"timestamp"`
}

// PrepareSessionV4Transaction materializes upstream entry/usage/value/list writes.
// Payloads are JSON values so custom fields and their serialization order survive.
//
//nolint:staticcheck // Error capitalization matches the upstream storage protocol.
func PrepareSessionV4Transaction(writes []json.RawMessage, firstSeq, timestamp int64) (SessionV4PreparedTransaction, error) {
	prepared := SessionV4PreparedTransaction{
		Writes: make([]json.RawMessage, 0, len(writes)),
		Result: SessionV4TransactionResult{FirstSeq: firstSeq, Seqs: make([]int64, 0, len(writes)), Timestamp: timestamp},
	}
	for index, write := range writes {
		seq := firstSeq + int64(index)
		_, fields, err := parseV4Object(write)
		if err != nil {
			return prepared, err
		}
		kind, _ := v4String(fields["kind"])
		members := []harnessJSONMember{{name: "kind", value: mustHarnessJSON(kind)}}
		switch kind {
		case "entry", "usage":
			payloadName := "entry"
			if kind == "usage" {
				payloadName = "row"
			}
			payload, _, payloadErr := parseV4Object(fields[payloadName])
			if payloadErr != nil {
				return prepared, payloadErr
			}
			for _, member := range payload {
				members = transactionSetMember(members, member)
			}
			members = transactionSetMember(members, harnessJSONMember{name: "seq", value: mustHarnessJSON(seq)})
			if kind == "entry" {
				members = transactionSetMember(members, harnessJSONMember{name: "timestamp", value: mustHarnessJSON(timestamp)})
			}
		case "value", "list":
			op, _ := v4String(fields["op"])
			members = append(members, harnessJSONMember{name: "op", value: mustHarnessJSON(op)}, harnessJSONMember{name: "seq", value: mustHarnessJSON(seq)})
			for _, field := range []string{"namespace", "key"} {
				if raw, ok := fields[field]; ok {
					members = append(members, harnessJSONMember{name: field, value: raw})
				}
			}
			if op == "set" || op == "append" {
				if raw, ok := fields["value"]; ok {
					members = append(members, harnessJSONMember{name: "value", value: raw})
				}
			}
		default:
			return prepared, fmt.Errorf("Invalid JSONL write kind: %s", kind)
		}
		encoded, marshalErr := marshalHarnessMembers(members)
		if marshalErr != nil {
			return prepared, marshalErr
		}
		encoded, marshalErr = partialjson.StringifyStreamingJSON(string(encoded))
		if marshalErr != nil {
			return prepared, marshalErr
		}
		prepared.Writes = append(prepared.Writes, encoded)
		prepared.Result.Seqs = append(prepared.Result.Seqs, seq)
	}
	return prepared, nil
}

func transactionSetMember(members []harnessJSONMember, member harnessJSONMember) []harnessJSONMember {
	for index := range members {
		if members[index].name == member.name {
			members[index] = member
			return members
		}
	}
	return append(members, member)
}

// ValidateSessionV4Transaction validates a whole transaction before any effects
// are published. IDs share a namespace between tree entries and usage rows.
//
//nolint:staticcheck // Error capitalization matches the upstream storage protocol.
func ValidateSessionV4Transaction(writes []json.RawMessage, firstSeq int64, hasID, hasEntry func(string) bool) error {
	previous := firstSeq - 1
	ids, entries := map[string]bool{}, map[string]bool{}
	for _, write := range writes {
		_, fields, err := parseV4Object(write)
		if err != nil {
			return err
		}
		seq, _ := v4SafeInteger(fields["seq"])
		if seq <= previous {
			return fmt.Errorf("Non-monotonic storage sequence: %d", seq)
		}
		previous = seq
		kind, _ := v4String(fields["kind"])
		if kind != "entry" && kind != "usage" {
			continue
		}
		id, _ := v4String(fields["id"])
		if hasID(id) || ids[id] {
			return fmt.Errorf("Duplicate entry or usage id: %s", id)
		}
		if kind == "entry" && !bytes.Equal(bytes.TrimSpace(fields["parentId"]), []byte("null")) {
			parent, ok := v4String(fields["parentId"])
			if !ok {
				parent = "undefined"
			}
			if !hasEntry(parent) && !entries[parent] {
				return fmt.Errorf("Missing parent entry: %s", parent)
			}
		}
		ids[id] = true
		if kind == "entry" {
			entries[id] = true
		}
	}
	return nil
}

// MarshalSessionV4Transaction returns one complete transaction line.
//
//nolint:staticcheck // Error capitalization matches the upstream storage protocol.
func MarshalSessionV4Transaction(writes []json.RawMessage) ([]byte, error) {
	if len(writes) == 1 {
		return append(bytes.Clone(writes[0]), '\n'), nil
	}
	var output bytes.Buffer
	output.WriteByte('[')
	for index, write := range writes {
		if !json.Valid(write) {
			return nil, fmt.Errorf("Invalid JSONL transaction write")
		}
		if index > 0 {
			output.WriteByte(',')
		}
		output.Write(write)
	}
	output.WriteString("]\n")
	return output.Bytes(), nil
}

func decodeTransactionEntry(raw json.RawMessage) (SessionV4Entry, error) {
	members, fields, err := parseV4Object(raw)
	if err != nil {
		return SessionV4Entry{}, err
	}
	entry, decodeErr := decodeV4EntryPayload(members, fields)
	if decodeErr != nil {
		return SessionV4Entry{}, decodeErr
	}
	return entry, nil
}
func decodeTransactionEntries(rows []json.RawMessage) ([]SessionV4Entry, error) {
	entries := make([]SessionV4Entry, 0, len(rows))
	for _, raw := range rows {
		entry, err := decodeTransactionEntry(raw)
		if err != nil {
			return nil, err
		}
		entries = append(entries, entry)
	}
	return entries, nil
}
