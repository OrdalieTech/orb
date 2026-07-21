package auth

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strconv"

	"github.com/OrdalieTech/pigo/internal/jsonwire"
)

func (credential *Credential) UnmarshalJSON(data []byte) error {
	members, order, err := decodeOrderedObject(data)
	if err != nil {
		return err
	}
	var decoded Credential
	decoded.order = order
	decoded.Extra = make(map[string]json.RawMessage)
	for _, name := range order {
		raw := members[name]
		switch name {
		case "type":
			if err := json.Unmarshal(raw, &decoded.Type); err != nil {
				return fmt.Errorf("auth credential type: %w", err)
			}
		case "key":
			if !bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
				var key string
				if err := json.Unmarshal(raw, &key); err != nil {
					return fmt.Errorf("auth credential key: %w", err)
				}
				decoded.Key = &key
			}
		case "env":
			members, envOrder, err := decodeOrderedObject(raw)
			if err != nil {
				return fmt.Errorf("auth credential env: %w", err)
			}
			decoded.Env = make(map[string]string, len(members))
			decoded.envOrder = envOrder
			for _, envName := range envOrder {
				var value string
				if err := json.Unmarshal(members[envName], &value); err != nil {
					return fmt.Errorf("auth credential env %q: %w", envName, err)
				}
				decoded.Env[envName] = value
			}
		case "refresh":
			if err := json.Unmarshal(raw, &decoded.Refresh); err != nil {
				return fmt.Errorf("auth credential refresh: %w", err)
			}
		case "access":
			if err := json.Unmarshal(raw, &decoded.Access); err != nil {
				return fmt.Errorf("auth credential access: %w", err)
			}
		case "expires":
			if err := json.Unmarshal(raw, &decoded.Expires); err != nil {
				return fmt.Errorf("auth credential expires: %w", err)
			}
		default:
			decoded.Extra[name] = append(json.RawMessage(nil), raw...)
		}
	}
	if len(decoded.Extra) == 0 {
		decoded.Extra = nil
	}
	*credential = decoded
	return nil
}

func (credential Credential) MarshalJSON() ([]byte, error) {
	order := append([]string(nil), credential.order...)
	appendMissing := func(name string, present bool) {
		if !present || contains(order, name) {
			return
		}
		order = append(order, name)
	}
	appendMissing("type", credential.Type != "")
	appendMissing("key", credential.Key != nil)
	appendMissing("env", credential.Env != nil)
	appendMissing("refresh", credential.Refresh != "")
	appendMissing("access", credential.Access != "")
	appendMissing("expires", credential.Expires != 0 || credential.Type == CredentialOAuth)
	extraNames := make([]string, 0, len(credential.Extra))
	for name := range credential.Extra {
		extraNames = append(extraNames, name)
	}
	sort.Strings(extraNames)
	for _, name := range extraNames {
		appendMissing(name, true)
	}

	var output bytes.Buffer
	output.WriteByte('{')
	written := 0
	for _, name := range order {
		value, include, err := credential.member(name)
		if err != nil {
			return nil, err
		}
		if !include {
			continue
		}
		if written > 0 {
			output.WriteByte(',')
		}
		key, err := jsonwire.Marshal(name)
		if err != nil {
			return nil, err
		}
		output.Write(key)
		output.WriteByte(':')
		output.Write(value)
		written++
	}
	output.WriteByte('}')
	return output.Bytes(), nil
}

func (credential Credential) member(name string) ([]byte, bool, error) {
	switch name {
	case "type":
		if credential.Type == "" {
			return nil, false, nil
		}
		value, err := jsonwire.Marshal(credential.Type)
		return value, true, err
	case "key":
		if credential.Key == nil {
			return nil, false, nil
		}
		value, err := jsonwire.Marshal(*credential.Key)
		return value, true, err
	case "env":
		if credential.Env == nil {
			return nil, false, nil
		}
		value, err := marshalStringMap(credential.Env, credential.envOrder)
		return value, true, err
	case "refresh":
		if credential.Refresh == "" && !contains(credential.order, name) {
			return nil, false, nil
		}
		value, err := jsonwire.Marshal(credential.Refresh)
		return value, true, err
	case "access":
		if credential.Access == "" && !contains(credential.order, name) {
			return nil, false, nil
		}
		value, err := jsonwire.Marshal(credential.Access)
		return value, true, err
	case "expires":
		if credential.Expires == 0 && credential.Type != CredentialOAuth && !contains(credential.order, name) {
			return nil, false, nil
		}
		return []byte(strconv.FormatInt(credential.Expires, 10)), true, nil
	default:
		value, exists := credential.Extra[name]
		return value, exists, nil
	}
}

func decodeOrderedObject(data []byte) (map[string]json.RawMessage, []string, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	token, err := decoder.Token()
	if err != nil {
		return nil, nil, err
	}
	if delimiter, ok := token.(json.Delim); !ok || delimiter != '{' {
		return nil, nil, fmt.Errorf("expected JSON object")
	}
	members := make(map[string]json.RawMessage)
	order := make([]string, 0)
	for decoder.More() {
		token, err = decoder.Token()
		if err != nil {
			return nil, nil, err
		}
		name, ok := token.(string)
		if !ok {
			return nil, nil, fmt.Errorf("expected JSON object key")
		}
		var raw json.RawMessage
		if err := decoder.Decode(&raw); err != nil {
			return nil, nil, err
		}
		if _, exists := members[name]; !exists {
			order = append(order, name)
		}
		members[name] = append(json.RawMessage(nil), raw...)
	}
	if _, err := decoder.Token(); err != nil {
		return nil, nil, err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err == nil {
		return nil, nil, fmt.Errorf("unexpected JSON trailer")
	} else if !errors.Is(err, io.EOF) {
		return nil, nil, fmt.Errorf("invalid JSON trailer: %w", err)
	}
	return members, order, nil
}

func marshalStringMap(values map[string]string, preferredOrder []string) ([]byte, error) {
	order := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, name := range preferredOrder {
		if _, exists := values[name]; !exists {
			continue
		}
		order = append(order, name)
		seen[name] = struct{}{}
	}
	remaining := make([]string, 0, len(values)-len(order))
	for name := range values {
		if _, exists := seen[name]; !exists {
			remaining = append(remaining, name)
		}
	}
	sort.Strings(remaining)
	order = append(order, remaining...)
	var output bytes.Buffer
	output.WriteByte('{')
	for index, name := range order {
		if index > 0 {
			output.WriteByte(',')
		}
		encodedName, err := jsonwire.Marshal(name)
		if err != nil {
			return nil, err
		}
		encodedValue, err := jsonwire.Marshal(values[name])
		if err != nil {
			return nil, err
		}
		output.Write(encodedName)
		output.WriteByte(':')
		output.Write(encodedValue)
	}
	output.WriteByte('}')
	return output.Bytes(), nil
}

func contains(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
