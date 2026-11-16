package auth

import (
	"context"
	"sort"
)

// Export returns the store's credentials as auth.json bytes, the set a Mitosis
// profile shares. Values are the stored form (an $ENV reference stays a reference),
// so nothing is resolved here.
func (store *Store) Export(ctx context.Context) ([]byte, error) {
	stored, err := store.load(ctx)
	if err != nil {
		return nil, err
	}
	return MarshalCredentials(stored.order, stored.byProvider)
}

// MergeIn atomically overlays overlay's credentials onto the store (overlay winning
// per provider), keeping credentials the overlay does not mention. It is how a peer
// applies a received vault without wiping its own local logins.
func (store *Store) MergeIn(ctx context.Context, overlay []byte) error {
	return store.document.Update(ctx, func(current []byte) ([]byte, error) {
		return MergeCredentials(current, overlay)
	})
}

// MergeCredentials overlays overlay's providers onto base, overlay winning per
// provider, and keeps base providers that overlay does not mention. Both inputs are
// auth.json bytes (empty is treated as "{}"); the result is auth.json bytes with
// providers in a stable order (base's providers first, then overlay-only ones, each
// group sorted). It never deletes a local credential the overlay is silent about, so
// syncing shared keys into a peer cannot wipe that peer's own logins.
func MergeCredentials(base, overlay []byte) ([]byte, error) {
	merged := map[string]*Credential{}
	var order []string
	add := func(data []byte) error {
		if len(data) == 0 {
			return nil
		}
		creds, err := ParseCredentials(data)
		if err != nil {
			return err
		}
		keys := make([]string, 0, len(creds))
		for k := range creds {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			if _, seen := merged[k]; !seen {
				order = append(order, k)
			}
			merged[k] = creds[k]
		}
		return nil
	}
	if err := add(base); err != nil {
		return nil, err
	}
	if err := add(overlay); err != nil {
		return nil, err
	}
	return MarshalCredentials(order, merged)
}
