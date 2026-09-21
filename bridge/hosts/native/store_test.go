package native

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/OrdalieTech/orb/bridge"
	"github.com/OrdalieTech/orb/connect"
	"github.com/OrdalieTech/orb/connect/protocol"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestStoreLockAndDurability(t *testing.T) {
	path := filepath.Join(t.TempDir(), "profile", "state.json")
	s, err := OpenStore(path, 4096)
	if err != nil {
		t.Fatal(err)
	}
	if other, err := OpenStore(path, 4096); err == nil {
		_ = other.Close()
		t.Fatal("second writer")
	}
	if err = s.Save([]byte(`{"version":1}`)); err != nil {
		t.Fatal(err)
	}
	_ = s.Close()
	s, err = OpenStore(path, 4096)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	b, err := s.Load()
	if err != nil || !bytes.Equal(b, []byte(`{"version":1}`)) {
		t.Fatal(string(b), err)
	}
	info, _ := os.Stat(path)
	if info.Mode().Perm() != 0600 {
		t.Fatal(info.Mode())
	}
	if err = s.Save(make([]byte, 4097)); err == nil {
		t.Fatal("quota bypass")
	}
}

func TestIPCSeparatesOwnerAndAttachmentCredentials(t *testing.T) {
	dir, err := os.MkdirTemp("", "orb-ipc-")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.RemoveAll(dir) }()
	store, err := OpenStore(filepath.Join(dir, "state.json"), protocol.MaxFrame)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	b, err := bridge.Open(store, true)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = b.Close() }()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	adminPath, attachPath := filepath.Join(dir, "admin.sock"), filepath.Join(dir, "attach.sock")
	closeAdmin, err := Listen(ctx, adminPath, b, "owner", b.Admin, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer closeAdmin()
	closeAttach, err := Listen(ctx, attachPath, b, "", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer closeAttach()
	if closeOther, err := Listen(ctx, adminPath, b, "owner", b.Admin, nil); err == nil {
		closeOther()
		t.Fatal("replaced live endpoint")
	}
	instance, credential, err := b.Enroll("test", b.PersonalGroup())
	if err != nil {
		t.Fatal(err)
	}
	if c, err := Dial(ctx, adminPath, Auth{Credential: credential}, nil, nil); err == nil {
		_ = c.Close()
		t.Fatal("instance gained owner access")
	}
	if c, err := Dial(ctx, attachPath, Auth{Credential: "owner", InstanceID: instance.ID, BootID: protocol.NewID()}, nil, nil); err == nil {
		_ = c.Close()
		t.Fatal("owner token accepted as instance credential")
	}
	var generation string
	c, err := Dial(ctx, attachPath, Auth{Credential: credential, InstanceID: instance.ID, BootID: protocol.NewID()}, func(context.Context, string, json.RawMessage) (json.RawMessage, error) {
		return connect.JSON(struct{}{}), nil
	}, func(g string) error { generation = g; return nil })
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Close() }()
	if generation == "" || !b.Available(instance.ID) {
		t.Fatal("registration unavailable")
	}
	if err = c.Call(ctx, "grant", struct{}{}, nil); err == nil {
		t.Fatal("attachment gained administration")
	}
	_ = c.Close()
	deadline := time.NewTimer(time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(time.Millisecond)
	defer tick.Stop()
	for b.Available(instance.ID) {
		select {
		case <-deadline.C:
			t.Fatal("disconnect remained available")
		case <-tick.C:
		}
	}
}
