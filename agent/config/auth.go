package config

import (
	"context"
	"errors"
	"os"
	"path/filepath"

	aiauth "github.com/OrdalieTech/orb/ai/auth"
	"github.com/OrdalieTech/orb/host"
)

// authDocument is auth.json's content in member order.
type authDocument struct {
	order       []string
	credentials map[string]*aiauth.Credential
}

// AuthStorage resolves config values in stored API keys on read.
type AuthStorage struct {
	*aiauth.Store
	path string
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
	// Like upstream ensureFileExists, seed "{}" so the file exists before the first write.
	if err := os.MkdirAll(filepath.Dir(resolved), 0o700); err != nil {
		return nil, err
	}
	file, err := os.OpenFile(resolved, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err == nil {
		_, err = file.WriteString("{}")
		err = errors.Join(err, file.Close())
	}
	if err != nil && !errors.Is(err, os.ErrExist) {
		return nil, err
	}
	return &AuthStorage{Store: aiauth.NewDocumentStore(authFile(resolved)), path: resolved}, nil
}

// NewAuthStorageWithDocument uses a caller-owned transactional credential document.
func NewAuthStorageWithDocument(document host.Document) (*AuthStorage, error) {
	if document == nil {
		return nil, errors.New("credential document is required")
	}
	result := &AuthStorage{Store: aiauth.NewDocumentStore(document)}
	if _, err := result.List(context.Background()); err != nil {
		return nil, err
	}
	return result, nil
}

func (storage *AuthStorage) Path() string { return storage.path }

func (storage *AuthStorage) Read(ctx context.Context, provider string) (*aiauth.Credential, error) {
	// Local credential reads survive RPC input closure.
	credential, err := storage.Store.Read(context.WithoutCancel(ctx), provider)
	return resolveStoredCredential(credential), err
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

func readStoredCredentials(path string) map[string]*aiauth.Credential {
	contents, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	credentials, err := aiauth.ParseCredentials(contents)
	if err != nil {
		return nil
	}
	return credentials
}

func emptyAuthDocument() authDocument {
	return authDocument{credentials: make(map[string]*aiauth.Credential)}
}

func marshalAuthDocument(document authDocument) ([]byte, error) {
	return aiauth.MarshalCredentials(document.order, document.credentials)
}
