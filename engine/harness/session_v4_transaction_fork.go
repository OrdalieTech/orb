package harness

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

type SessionV4TransactionForkOptions struct {
	Scope    string  `json:"scope"`
	Branch   string  `json:"branch,omitempty"`
	EntryID  *string `json:"entryId,omitempty"`
	Position string  `json:"position,omitempty"`
}
type SessionV4TransactionSnapshot struct {
	Entries      []json.RawMessage      `json:"entries"`
	ScalarValues []SessionV4StoredValue `json:"scalarValues"`
}

func (storage *TransactionSessionV4Storage) CaptureForkSource() (SessionV4TransactionSnapshot, error) {
	storage.mu.Lock()
	defer storage.mu.Unlock()
	if err := storage.assertOpen(); err != nil {
		return SessionV4TransactionSnapshot{}, err
	}
	snapshot := SessionV4TransactionSnapshot{Entries: []json.RawMessage{}, ScalarValues: []SessionV4StoredValue{}}
	for _, id := range storage.state.entryOrder {
		snapshot.Entries = append(snapshot.Entries, bytes.Clone(storage.state.entries[id]))
	}
	for _, key := range storage.state.valueOrder {
		value := storage.state.values[key]
		value.Value = bytes.Clone(value.Value)
		snapshot.ScalarValues = append(snapshot.ScalarValues, value)
	}
	return snapshot, nil
}

//nolint:staticcheck // Error capitalization matches the upstream fork protocol.
func createTransactionFork(source SessionV4TransactionSnapshot, options SessionV4TransactionForkOptions) ([]json.RawMessage, int64, error) {
	entries := map[string]json.RawMessage{}
	values := map[string]SessionV4StoredValue{}
	tips := []SessionV4StoredValue{}
	tipKeys := map[string]bool{}
	for _, entry := range source.Entries {
		entries[transactionString(transactionFields(entry), "id")] = entry
	}
	for _, value := range source.ScalarValues {
		values[transactionKey(value.Address)] = value
		if value.Address.Namespace == "pi.branch.tip" {
			tips = append(tips, value)
			tipKeys[value.Address.Key] = true
		}
	}
	for _, value := range source.ScalarValues {
		if (value.Address.Namespace == "pi.lane.config" || value.Address.Namespace == "pi.lane.state") && !tipKeys[value.Address.Key] {
			return nil, 0, fmt.Errorf("Source session branch %q is missing branch.tip", value.Address.Key)
		}
	}
	for _, tip := range tips {
		_, config := values["pi.lane.config\x00"+tip.Address.Key]
		_, state := values["pi.lane.state\x00"+tip.Address.Key]
		if config != state {
			return nil, 0, fmt.Errorf("Source session branch %q has incomplete lane state", tip.Address.Key)
		}
		if options.Scope == "branch" && tip.Address.Key == options.Branch && !config {
			return nil, 0, fmt.Errorf("Source branch %q is not a configured AgentLane", tip.Address.Key)
		}
		if !bytes.Equal(tip.Value, []byte("null")) {
			id, _ := v4String(tip.Value)
			if entries[id] == nil {
				return nil, 0, fmt.Errorf("Source session branch %q has an unknown tip", tip.Address.Key)
			}
		}
	}
	copied := map[string]bool{}
	destinationTips := []SessionV4StoredValue{}
	if options.Scope == "tree" {
		for id := range entries {
			copied[id] = true
		}
		destinationTips = tips
	} else {
		var sourceTip *SessionV4StoredValue
		for i := range tips {
			if tips[i].Address.Key == options.Branch {
				sourceTip = &tips[i]
				break
			}
		}
		if sourceTip == nil {
			return nil, 0, fmt.Errorf("Unknown source branch: %s", options.Branch)
		}
		requested := sourceTip.Value
		if options.EntryID != nil {
			requested = mustHarnessJSON(*options.EntryID)
		}
		found := bytes.Equal(requested, []byte("null"))
		tip := json.RawMessage("null")
		current := sourceTip.Value
		for !bytes.Equal(current, []byte("null")) {
			id, _ := v4String(current)
			entry := entries[id]
			if entry == nil {
				return nil, 0, fmt.Errorf("Corrupt source branch: missing parent %s", id)
			}
			fields := transactionFields(entry)
			if bytes.Equal(current, requested) {
				found = true
				if options.Position == "before" {
					tip = fields["parentId"]
				} else {
					tip = current
					copied[id] = true
				}
			} else if found {
				copied[id] = true
			}
			current = fields["parentId"]
		}
		if !found {
			id, _ := v4String(requested)
			return nil, 0, fmt.Errorf("Fork entry %s is not on source branch %q", id, options.Branch)
		}
		destinationTips = append(destinationTips, SessionV4StoredValue{Address: SessionV4Address{Namespace: "pi.branch.tip", Key: options.Branch, Kind: "value"}, Value: tip})
	}
	writes := []json.RawMessage{}
	nextSeq := int64(1)
	for _, entry := range source.Entries {
		id := transactionString(transactionFields(entry), "id")
		if !copied[id] {
			continue
		}
		members, fields, _ := parseV4Object(entry)
		members = append([]harnessJSONMember{{name: "kind", value: mustHarnessJSON("entry")}}, members...)
		raw, _ := marshalHarnessMembers(members)
		writes = append(writes, raw)
		if seq := transactionSeq(fields); seq >= nextSeq {
			nextSeq = seq + 1
		}
	}
	store := func(namespace, key string, value any) {
		writes = append(writes, transactionObject("kind", "value", "op", "set", "seq", nextSeq, "namespace", namespace, "key", key, "value", value))
		nextSeq++
	}
	for _, tip := range destinationTips {
		store("pi.branch.tip", tip.Address.Key, tip.Value)
		if config, ok := values["pi.lane.config\x00"+tip.Address.Key]; ok {
			store("pi.lane.config", tip.Address.Key, config.Value)
			store("pi.lane.state", tip.Address.Key, transactionObject("currentOperationId", nil, "lastOperationId", nil, "inbox", []any{}))
		}
	}
	for _, value := range source.ScalarValues {
		namespace := value.Address.Namespace
		switch namespace {
		case "pi.session.name":
			store(namespace, value.Address.Key, value.Value)
		case "pi.entry.label":
			if copied[value.Address.Key] {
				store(namespace, value.Address.Key, value.Value)
			}
		case "pi.branch.tip", "pi.lane.config", "pi.lane.state", "pi.result":
		default:
			if strings.HasPrefix(namespace, "pi.op.") || strings.HasPrefix(namespace, "pi.pending.") {
				continue
			}
			if namespace == "pi" || strings.HasPrefix(namespace, "pi.") {
				return nil, 0, fmt.Errorf("Unknown reserved fork namespace: %s", namespace)
			}
			if options.Scope == "tree" {
				store(namespace, value.Address.Key, value.Value)
			}
		}
	}
	sort.Slice(writes, func(i, j int) bool {
		return transactionSeq(transactionFields(writes[i])) < transactionSeq(transactionFields(writes[j]))
	})
	return writes, nextSeq, nil
}
func (storage *TransactionSessionV4Storage) Fork(ctx context.Context, fs FileSystem, path string, header SessionV4TransactionHeader, options SessionV4TransactionForkOptions) (*TransactionSessionV4Storage, error) {
	snapshot, err := storage.CaptureForkSource()
	if err != nil {
		return nil, err
	}
	writes, nextSeq, err := createTransactionFork(snapshot, options)
	if err != nil {
		return nil, err
	}
	header.NextSeq = &nextSeq
	if fs == nil {
		target := NewMemorySessionV4TransactionStorage()
		if err = target.state.validate(writes); err != nil {
			return nil, err
		}
		target.state.apply(writes)
		target.state.nextSeq = nextSeq
		return target, nil
	}
	encoded, err := marshalHarnessValue(header)
	if err != nil {
		return nil, err
	}
	content := append(encoded, '\n')
	for _, write := range writes {
		content = append(content, write...)
		content = append(content, '\n')
	}
	if err = publishTransactionFile(ctx, fs, path, content); err != nil {
		return nil, err
	}
	return OpenJSONLSessionV4TransactionStorage(ctx, fs, path, storage.Now)
}
