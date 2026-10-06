package config

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"

	"github.com/OrdalieTech/orb/host"
	"github.com/OrdalieTech/orb/internal/jsonwire"

	"github.com/OrdalieTech/orb/ai"
	aiauth "github.com/OrdalieTech/orb/ai/auth"
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
			settings, _ := jsonwire.ParseRawObject(normalized)
			if raw, exists := settings.Get("apiKeys"); exists {
				if added, ok := addLegacyAPIKeys(&document, raw); ok {
					migrated = append(migrated, added...)
					settings.Delete("apiKeys")
					encoded, marshalErr := marshalStringifiedObject(settings)
					if marshalErr == nil {
						// settings.json already exists, so WriteFile preserves
						// its mode just like upstream writeFileSync.
						_ = os.WriteFile(settingsPath, encoded, 0o644)
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

// marshalStringifiedObject writes object as JSON.stringify(object, null, 2).
func marshalStringifiedObject(object jsonwire.RawObject) ([]byte, error) {
	compact, _ := object.MarshalJSON()
	normalized, err := ai.NormalizeJSONStringifyJSON(compact)
	if err != nil {
		return nil, err
	}
	var indented bytes.Buffer
	if err := json.Indent(&indented, normalized, "", "  "); err != nil {
		return nil, err
	}
	return indented.Bytes(), nil
}

// addLegacyAPIKeys adds the string members of the legacy settings apiKeys
// object raw that document has no credential for, returning their providers;
// ok is false when raw is not a JSON object.
func addLegacyAPIKeys(document *authDocument, raw json.RawMessage) (added []string, ok bool) {
	keys, ok := jsonwire.ParseRawObject(raw)
	for _, member := range keys {
		var key string
		if _, exists := document.credentials[member.Name]; exists || json.Unmarshal(member.Value, &key) != nil {
			continue
		}
		document.order = append(document.order, member.Name)
		document.credentials[member.Name] = aiauth.APIKeyCredential(key)
		added = append(added, member.Name)
	}
	return added, ok
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

// MigrateAuthDocuments upgrades legacy credentials during an offline native
// migration. Original files are never renamed or rewritten.
func MigrateAuthDocuments(ctx context.Context, auth, settings, oauth host.Document) error {
	return auth.Update(ctx, func(current []byte) ([]byte, error) {
		if len(current) > 0 {
			return current, nil
		}
		result := emptyAuthDocument()
		data, err := oauth.Read(ctx)
		if err != nil {
			return nil, err
		}
		if len(data) > 0 {
			legacy, err := parseLegacyOAuth(data)
			if err != nil {
				return nil, err
			}
			result = legacy
		}
		data, err = settings.Read(ctx)
		if err != nil {
			return nil, err
		}
		if len(data) > 0 {
			settings, ok := jsonwire.ParseRawObject(data)
			if !ok {
				return nil, errors.New("settings.json is not a JSON object")
			}
			if raw, _ := settings.Get("apiKeys"); len(raw) > 0 {
				if _, ok := addLegacyAPIKeys(&result, raw); !ok {
					return nil, errors.New("settings.json apiKeys is not a JSON object")
				}
			}
		}
		if len(result.order) == 0 {
			return nil, nil
		}
		return marshalAuthDocument(result)
	})
}
