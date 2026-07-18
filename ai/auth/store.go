package auth

import (
	"context"
	"sync"
)

type MemoryStore struct {
	mu          sync.RWMutex
	credentials map[string]*Credential
	order       []string
}

func NewMemoryStore(initial map[string]*Credential) *MemoryStore {
	store := &MemoryStore{credentials: make(map[string]*Credential, len(initial))}
	for provider, credential := range initial {
		store.credentials[provider] = credential.Clone()
		store.order = append(store.order, provider)
	}
	return store
}

func (store *MemoryStore) Read(_ context.Context, provider string) (*Credential, error) {
	store.mu.RLock()
	defer store.mu.RUnlock()
	return store.credentials[provider].Clone(), nil
}

func (store *MemoryStore) List(_ context.Context) ([]CredentialInfo, error) {
	store.mu.RLock()
	defer store.mu.RUnlock()
	result := make([]CredentialInfo, 0, len(store.order))
	for _, provider := range store.order {
		if credential := store.credentials[provider]; credential != nil {
			result = append(result, CredentialInfo{ProviderID: provider, Type: credential.Type})
		}
	}
	return result, nil
}

func (store *MemoryStore) Modify(_ context.Context, provider string, modify ModifyFunc) (*Credential, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	current := store.credentials[provider].Clone()
	next, err := modify(current)
	if err != nil {
		return nil, err
	}
	if next == nil {
		return current, nil
	}
	if _, exists := store.credentials[provider]; !exists {
		store.order = append(store.order, provider)
	}
	store.credentials[provider] = next.Clone()
	return next.Clone(), nil
}

func (store *MemoryStore) Delete(_ context.Context, provider string) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	delete(store.credentials, provider)
	for index, item := range store.order {
		if item == provider {
			store.order = append(store.order[:index], store.order[index+1:]...)
			break
		}
	}
	return nil
}
