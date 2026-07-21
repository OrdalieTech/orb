package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"

	"github.com/OrdalieTech/pigo/ai"
	aiauth "github.com/OrdalieTech/pigo/ai/auth"
)

func MigrateAuthToAuthJSON(agentDir string) ([]string, error) {
	authPath := filepath.Join(agentDir, "auth.json")
	if _, err := os.Stat(authPath); err == nil {
		return nil, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}

	document := emptyAuthDocument()
	migrated := make([]string, 0)
	oauthPath := filepath.Join(agentDir, "oauth.json")
	if contents, err := os.ReadFile(oauthPath); err == nil {
		if legacy, parseErr := parseLegacyOAuth(contents); parseErr == nil {
			for _, provider := range legacy.order {
				document.order = append(document.order, provider)
				document.credentials[provider] = legacy.credentials[provider]
				migrated = append(migrated, provider)
			}
			_ = os.Rename(oauthPath, oauthPath+".migrated")
		}
	}

	settingsPath := filepath.Join(agentDir, "settings.json")
	if contents, err := os.ReadFile(settingsPath); err == nil {
		if normalized, normalizeErr := ai.NormalizeJSONStringifyJSON(contents); normalizeErr == nil {
			order, settings, parseErr := parseOrderedRawObject(normalized)
			if raw, exists := settings["apiKeys"]; parseErr == nil && exists {
				keyOrder, keys, keysErr := parseOrderedRawObject(raw)
				if keysErr == nil {
					for _, provider := range keyOrder {
						var key string
						if json.Unmarshal(keys[provider], &key) != nil {
							continue
						}
						if _, exists := document.credentials[provider]; exists {
							continue
						}
						document.order = append(document.order, provider)
						document.credentials[provider] = aiauth.APIKeyCredential(key)
						migrated = append(migrated, provider)
					}
					delete(settings, "apiKeys")
					order = removeOrderedName(order, "apiKeys")
					encoded, marshalErr := marshalOrderedRawObject(order, settings)
					if marshalErr == nil {
						_ = os.WriteFile(settingsPath, encoded, 0o600)
					}
				}
			}
		}
	}

	if len(migrated) == 0 {
		return nil, nil
	}
	if err := os.MkdirAll(agentDir, 0o700); err != nil {
		return nil, err
	}
	encoded, err := marshalAuthDocument(document)
	if err != nil {
		return nil, err
	}
	if err := os.WriteFile(authPath, encoded, 0o600); err != nil {
		return nil, err
	}
	return migrated, nil
}

func parseOrderedRawObject(data []byte) ([]string, map[string]json.RawMessage, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	token, err := decoder.Token()
	if err != nil {
		return nil, nil, err
	}
	if delimiter, ok := token.(json.Delim); !ok || delimiter != '{' {
		return nil, nil, errors.New("expected JSON object")
	}
	order := make([]string, 0)
	members := make(map[string]json.RawMessage)
	for decoder.More() {
		key, err := decoder.Token()
		if err != nil {
			return nil, nil, err
		}
		name, ok := key.(string)
		if !ok {
			return nil, nil, errors.New("expected JSON object key")
		}
		var raw json.RawMessage
		if err := decoder.Decode(&raw); err != nil {
			return nil, nil, err
		}
		if _, exists := members[name]; !exists {
			order = append(order, name)
		}
		members[name] = raw
	}
	if _, err := decoder.Token(); err != nil {
		return nil, nil, err
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		if err == nil {
			return nil, nil, errors.New("multiple JSON values")
		}
		return nil, nil, err
	}
	return order, members, nil
}

func marshalOrderedRawObject(order []string, members map[string]json.RawMessage) ([]byte, error) {
	var compact bytes.Buffer
	compact.WriteByte('{')
	for index, name := range order {
		if index > 0 {
			compact.WriteByte(',')
		}
		encodedName, err := json.Marshal(name)
		if err != nil {
			return nil, err
		}
		compact.Write(encodedName)
		compact.WriteByte(':')
		compact.Write(members[name])
	}
	compact.WriteByte('}')
	normalized, err := ai.NormalizeJSONStringifyJSON(compact.Bytes())
	if err != nil {
		return nil, err
	}
	var indented bytes.Buffer
	if err := json.Indent(&indented, normalized, "", "  "); err != nil {
		return nil, err
	}
	return indented.Bytes(), nil
}

func removeOrderedName(order []string, name string) []string {
	for index, candidate := range order {
		if candidate == name {
			return append(order[:index], order[index+1:]...)
		}
	}
	return order
}

func parseLegacyOAuth(data []byte) (authDocument, error) {
	normalized, err := ai.NormalizeJSONStringifyJSON(data)
	if err != nil {
		return authDocument{}, err
	}
	decoder := json.NewDecoder(bytes.NewReader(normalized))
	token, err := decoder.Token()
	if err != nil {
		return authDocument{}, err
	}
	if delimiter, ok := token.(json.Delim); !ok || delimiter != '{' {
		return authDocument{}, errors.New("oauth.json must contain a JSON object")
	}
	document := emptyAuthDocument()
	for decoder.More() {
		key, err := decoder.Token()
		if err != nil {
			return authDocument{}, err
		}
		provider := key.(string)
		var raw json.RawMessage
		if err := decoder.Decode(&raw); err != nil {
			return authDocument{}, err
		}
		trimmed := bytes.TrimSpace(raw)
		if len(trimmed) < 2 || trimmed[0] != '{' || trimmed[len(trimmed)-1] != '}' {
			return authDocument{}, errors.New("oauth.json credential must be a JSON object")
		}
		encoded := append([]byte(`{"type":"oauth"`), func() []byte {
			body := bytes.TrimSpace(trimmed[1 : len(trimmed)-1])
			if len(body) == 0 {
				return []byte("}")
			}
			return append(append([]byte{','}, body...), '}')
		}()...)
		var credential aiauth.Credential
		if err := json.Unmarshal(encoded, &credential); err != nil {
			return authDocument{}, err
		}
		document.order = append(document.order, provider)
		document.credentials[provider] = &credential
	}
	_, err = decoder.Token()
	return document, err
}
