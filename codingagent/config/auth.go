package config

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/OrdalieTech/pigo/ai"
	aiauth "github.com/OrdalieTech/pigo/ai/auth"
	"github.com/OrdalieTech/pigo/internal/jsonwire"
)

type authDocument struct {
	order       []string
	credentials map[string]*aiauth.Credential
}

type AuthStorage struct {
	path string

	mu   sync.RWMutex
	data authDocument
}

func NewAuthStorage(path string) (*AuthStorage, error) {
	resolved, err := NormalizePath(path)
	if err != nil {
		return nil, err
	}
	resolved, err = filepath.Abs(resolved)
	if err != nil {
		return nil, err
	}
	storage := &AuthStorage{path: resolved, data: emptyAuthDocument()}
	if err := storage.ensureFile(); err != nil {
		return nil, err
	}
	storage.Reload()
	return storage, nil
}

func NewDefaultAuthStorage() (*AuthStorage, error) {
	agentDir, err := GetAgentDir()
	if err != nil {
		return nil, err
	}
	return NewAuthStorage(filepath.Join(agentDir, "auth.json"))
}

func (storage *AuthStorage) Path() string { return storage.path }

func (storage *AuthStorage) Reload() {
	document, err := storage.readLocked(context.Background())
	if err != nil {
		return
	}
	storage.mu.Lock()
	storage.data = document
	storage.mu.Unlock()
}

func (storage *AuthStorage) Read(_ context.Context, provider string) (*aiauth.Credential, error) {
	storage.mu.RLock()
	credential := storage.data.credentials[provider].Clone()
	storage.mu.RUnlock()
	return resolveStoredCredential(credential), nil
}

func (storage *AuthStorage) List(_ context.Context) ([]aiauth.CredentialInfo, error) {
	storage.mu.RLock()
	defer storage.mu.RUnlock()
	result := make([]aiauth.CredentialInfo, 0, len(storage.data.order))
	for _, provider := range storage.data.order {
		credential := storage.data.credentials[provider]
		if credential != nil {
			result = append(result, aiauth.CredentialInfo{ProviderID: provider, Type: credential.Type})
		}
	}
	return result, nil
}

func (storage *AuthStorage) Modify(
	ctx context.Context,
	provider string,
	modify aiauth.ModifyFunc,
) (*aiauth.Credential, error) {
	var result *aiauth.Credential
	err := storage.withLock(ctx, func(current []byte) ([]byte, bool, error) {
		document, err := parseAuthDocument(current)
		if err != nil {
			return nil, false, err
		}
		currentCredential := document.credentials[provider].Clone()
		next, err := modify(currentCredential)
		if err != nil {
			return nil, false, err
		}
		if next == nil {
			storage.setSnapshot(document)
			result = currentCredential
			return nil, false, nil
		}
		if _, exists := document.credentials[provider]; !exists {
			document.order = append(document.order, provider)
		}
		document.credentials[provider] = next.Clone()
		encoded, err := marshalAuthDocument(document)
		if err != nil {
			return nil, false, err
		}
		storage.setSnapshot(document)
		result = next.Clone()
		return encoded, true, nil
	})
	return result, err
}

func (storage *AuthStorage) Delete(ctx context.Context, provider string) error {
	return storage.withLock(ctx, func(current []byte) ([]byte, bool, error) {
		document, err := parseAuthDocument(current)
		if err != nil {
			return nil, false, err
		}
		delete(document.credentials, provider)
		for index, item := range document.order {
			if item == provider {
				document.order = append(document.order[:index], document.order[index+1:]...)
				break
			}
		}
		encoded, err := marshalAuthDocument(document)
		if err != nil {
			return nil, false, err
		}
		storage.setSnapshot(document)
		return encoded, true, nil
	})
}

func ReadStoredCredential(provider, path string) *aiauth.Credential {
	resolved, err := NormalizePath(path)
	if err != nil {
		return nil
	}
	contents, err := os.ReadFile(resolved)
	if err != nil {
		return nil
	}
	document, err := parseAuthDocument(contents)
	if err != nil {
		return nil
	}
	return document.credentials[provider].Clone()
}

func readStoredCredentials(path string) map[string]*aiauth.Credential {
	contents, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	document, err := parseAuthDocument(contents)
	if err != nil {
		return nil
	}
	result := make(map[string]*aiauth.Credential, len(document.credentials))
	for provider, credential := range document.credentials {
		if credential != nil {
			result[provider] = credential.Clone()
		}
	}
	return result
}

func resolveStoredCredential(credential *aiauth.Credential) *aiauth.Credential {
	resolvedCredential := credential.Clone()
	if resolvedCredential == nil || resolvedCredential.Type != aiauth.CredentialAPIKey || resolvedCredential.Key == nil {
		return resolvedCredential
	}
	resolved, ok := ResolveAuthConfigValue(*resolvedCredential.Key, resolvedCredential.Env)
	if ok {
		resolvedCredential.Key = &resolved
	} else {
		resolvedCredential.Key = nil
	}
	return resolvedCredential
}

func (storage *AuthStorage) readLocked(ctx context.Context) (authDocument, error) {
	var result authDocument
	err := storage.withLock(ctx, func(current []byte) ([]byte, bool, error) {
		document, err := parseAuthDocument(current)
		if err != nil {
			return nil, false, err
		}
		result = document
		return nil, false, nil
	})
	return result, err
}

func (storage *AuthStorage) withLock(
	ctx context.Context,
	operation func(current []byte) (next []byte, write bool, err error),
) error {
	if err := storage.ensureFile(); err != nil {
		return err
	}
	lockContext := ctx
	if _, hasDeadline := ctx.Deadline(); !hasDeadline {
		var cancel context.CancelFunc
		lockContext, cancel = context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
	}
	lock, err := acquireAuthDirectoryLock(lockContext, storage.path)
	if err != nil {
		return err
	}
	operationErr := func() error {
		current, err := os.ReadFile(storage.path)
		if err != nil {
			return err
		}
		next, write, err := operation(current)
		if err != nil {
			return err
		}
		if err := lock.Check(); err != nil {
			return err
		}
		if !write {
			return nil
		}
		if err := os.WriteFile(storage.path, next, 0o600); err != nil {
			return err
		}
		if err := os.Chmod(storage.path, 0o600); err != nil {
			return err
		}
		return lock.Check()
	}()
	// Upstream proper-lockfile suppresses unlock failures in the async finally path.
	_ = lock.Release()
	if operationErr != nil {
		return operationErr
	}
	return nil
}

func (storage *AuthStorage) ensureFile() error {
	if err := os.MkdirAll(filepath.Dir(storage.path), 0o700); err != nil {
		return err
	}
	file, err := os.OpenFile(storage.path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err == nil {
		if _, writeErr := file.WriteString("{}"); writeErr != nil {
			_ = file.Close()
			return writeErr
		}
		return file.Close()
	}
	if errors.Is(err, os.ErrExist) {
		return nil
	}
	return err
}

func (storage *AuthStorage) setSnapshot(document authDocument) {
	storage.mu.Lock()
	storage.data = cloneAuthDocument(document)
	storage.mu.Unlock()
}

func emptyAuthDocument() authDocument {
	return authDocument{credentials: make(map[string]*aiauth.Credential)}
}

func cloneAuthDocument(document authDocument) authDocument {
	cloned := authDocument{
		order:       append([]string(nil), document.order...),
		credentials: make(map[string]*aiauth.Credential, len(document.credentials)),
	}
	for provider, credential := range document.credentials {
		cloned.credentials[provider] = credential.Clone()
	}
	return cloned
}

func parseAuthDocument(data []byte) (authDocument, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	token, err := decoder.Token()
	if err != nil {
		return authDocument{}, err
	}
	if delimiter, ok := token.(json.Delim); !ok || delimiter != '{' {
		return authDocument{}, errors.New("auth.json must contain a JSON object")
	}
	document := emptyAuthDocument()
	for decoder.More() {
		token, err := decoder.Token()
		if err != nil {
			return authDocument{}, err
		}
		provider, ok := token.(string)
		if !ok {
			return authDocument{}, errors.New("auth.json contains an invalid provider key")
		}
		var raw json.RawMessage
		if err := decoder.Decode(&raw); err != nil {
			return authDocument{}, err
		}
		var credential aiauth.Credential
		if err := json.Unmarshal(raw, &credential); err != nil {
			return authDocument{}, fmt.Errorf("auth.json provider %q: %w", provider, err)
		}
		if _, exists := document.credentials[provider]; !exists {
			document.order = append(document.order, provider)
		}
		document.credentials[provider] = &credential
	}
	if _, err := decoder.Token(); err != nil {
		return authDocument{}, err
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		if err == nil {
			return authDocument{}, errors.New("auth.json contains multiple JSON values")
		}
		return authDocument{}, err
	}
	return document, nil
}

func marshalAuthDocument(document authDocument) ([]byte, error) {
	var compact bytes.Buffer
	compact.WriteByte('{')
	written := 0
	for _, provider := range document.order {
		credential := document.credentials[provider]
		if credential == nil {
			continue
		}
		if written > 0 {
			compact.WriteByte(',')
		}
		name, err := jsonwire.Marshal(provider)
		if err != nil {
			return nil, err
		}
		value, err := credential.MarshalJSON()
		if err != nil {
			return nil, err
		}
		compact.Write(name)
		compact.WriteByte(':')
		compact.Write(value)
		written++
	}
	compact.WriteByte('}')
	normalized, err := ai.NormalizeJSONStringifyJSON(compact.Bytes())
	if err != nil {
		return nil, err
	}
	if written == 0 {
		return normalized, nil
	}
	var indented bytes.Buffer
	if err := json.Indent(&indented, normalized, "", "  "); err != nil {
		return nil, err
	}
	return indented.Bytes(), nil
}
