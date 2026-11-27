package view

import (
	"context"
	"encoding/json"
	"slices"
	"sync"
	"testing"

	"github.com/OrdalieTech/orb/mitosis"
)

type fakeArchiveHost struct {
	mu       sync.Mutex
	archived map[string]bool // session_id -> archived
	sessions []struct {
		ID       string `json:"session_id"`
		Name     string `json:"name"`
		CWD      string `json:"cwd"`
		First    string `json:"first"`
		Modified int64  `json:"modified"`
	}
	archiveCalls []map[string]any
}

func (f *fakeArchiveHost) call(_ context.Context, method string, params, result any) error {
	raw, _ := json.Marshal(params)
	var p map[string]any
	_ = json.Unmarshal(raw, &p)

	switch method {
	case "status":
		return json.Unmarshal([]byte(`{"peer_id":"p0","peers":["p1"],"peer_states":{"p0":"connected","p1":"connected"}}`), result)
	case "remote":
		peer, _ := p["peer_id"].(string)
		rmethod, _ := p["method"].(string)
		rparams, _ := p["params"].(map[string]any)
		switch rmethod {
		case "agent.describe":
			return json.Unmarshal([]byte(`{"items":[]}`), result)
		case "mitosis.ping":
			return json.Unmarshal([]byte(`{"name":"peer"}`), result)
		case "host.sessions":
			if peer == "p1" {
				// Simulate a peer with one archived session and one active session.
				wantArchived, _ := rparams["archived"].(bool)
				if wantArchived {
					return json.Unmarshal([]byte(`{"host":"lab","version":"1.0","items":[{"session_id":"s-remote","name":"Remote Old","cwd":"/srv/app","modified":300}]}`), result)
				}
				return json.Unmarshal([]byte(`{"host":"lab","version":"1.0","items":[{"session_id":"s-remote-active","name":"Remote Active","cwd":"/srv/app","modified":400}]}`), result)
			}
			f.mu.Lock()
			defer f.mu.Unlock()
			wantArchived, _ := rparams["archived"].(bool)
			var items []map[string]any
			for _, s := range f.sessions {
				if f.archived[s.ID] == wantArchived {
					items = append(items, map[string]any{
						"session_id": s.ID,
						"name":       s.Name,
						"cwd":        s.CWD,
						"first":      s.First,
						"modified":   s.Modified,
					})
				}
			}
			b, _ := json.Marshal(map[string]any{"host": "mac", "version": "1.0", "items": items})
			return json.Unmarshal(b, result)
		case "host.session.archive":
			f.mu.Lock()
			defer f.mu.Unlock()
			id, _ := rparams["id"].(string)
			arch, _ := rparams["archived"].(bool)
			f.archived[id] = arch
			f.archiveCalls = append(f.archiveCalls, map[string]any{"peer": peer, "id": id, "archived": arch})
			if result != nil {
				_ = json.Unmarshal([]byte(`{"ok":true}`), result)
			}
			return nil
		default:
			return mitosis.Fail("method_not_found")
		}
	}
	return nil
}

func TestArchiveAndUnarchiveIntents(t *testing.T) {
	host := &fakeArchiveHost{
		archived: map[string]bool{"s3": true},
		sessions: []struct {
			ID       string `json:"session_id"`
			Name     string `json:"name"`
			CWD      string `json:"cwd"`
			First    string `json:"first"`
			Modified int64  `json:"modified"`
		}{
			{ID: "s1", Name: "First", CWD: "/work/a", Modified: 200},
			{ID: "s2", Name: "Second", CWD: "/work/b", Modified: 100},
			{ID: "s3", Name: "Already Archived", CWD: "/work/c", Modified: 150},
		},
	}

	c := newClient()
	app := New(t.Context(), Options{
		Call: host.call,
		Name: "mac",
		Emit: func(msg any) {
			c.out <- mitosis.JSON(msg)
		},
	})

	// Wait until initial threads are loaded in Home.
	c.until(t, "initial threads loaded", func() bool {
		return slices.ContainsFunc(c.home, func(e Entry) bool { return e.Key == "t:p0|s1" }) &&
			slices.ContainsFunc(c.home, func(e Entry) bool { return e.Key == "t:p0|s2" })
	})

	// 1. Archive s1 via key.
	app.Do(mitosis.JSON(map[string]any{"id": "a1", "do": "archive", "key": "t:p0|s1"}))
	c.until(t, "s1 archived and removed from home", func() bool {
		_, replied := c.replies["a1"]
		return replied && !slices.ContainsFunc(c.home, func(e Entry) bool { return e.Key == "t:p0|s1" })
	})

	// 2. Archive s2 via {machine, session_id, archived: true}.
	app.Do(mitosis.JSON(map[string]any{"id": "a2", "do": "archive", "machine": "p0", "session_id": "s2", "archived": true}))
	c.until(t, "s2 archived and removed from home", func() bool {
		_, replied := c.replies["a2"]
		return replied && !slices.ContainsFunc(c.home, func(e Entry) bool { return e.Key == "t:p0|s2" })
	})

	// 3. Query threads.archived across connected machines and verify newest-first ordering.
	app.Do(mitosis.JSON(map[string]any{"id": "q1", "do": "threads.archived"}))
	c.until(t, "threads.archived reply", func() bool {
		_, ok := c.replies["q1"]
		return ok
	})
	var qRes struct {
		Entries []Entry `json:"entries"`
	}
	if err := json.Unmarshal(c.replies["q1"], &qRes); err != nil {
		t.Fatalf("unmarshal threads.archived: %v", err)
	}
	wantKeys := []string{"t:p1|s-remote", "t:p0|s1", "t:p0|s3", "t:p0|s2"}
	gotKeys := make([]string, len(qRes.Entries))
	for i, e := range qRes.Entries {
		gotKeys[i] = e.Key
	}
	if !slices.Equal(gotKeys, wantKeys) {
		t.Fatalf("archived entries = %v, want %v", gotKeys, wantKeys)
	}

	// 4. Unarchive s1 with archived: false and verify reload restores it in Home.
	app.Do(mitosis.JSON(map[string]any{"id": "u1", "do": "archive", "key": "t:p0|s1", "archived": false}))
	c.until(t, "s1 unarchived and restored in home", func() bool {
		_, replied := c.replies["u1"]
		return replied && slices.ContainsFunc(c.home, func(e Entry) bool { return e.Key == "t:p0|s1" })
	})
}
