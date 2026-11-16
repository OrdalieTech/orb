package daemon

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"time"

	"github.com/OrdalieTech/orb/ai/auth"
	"github.com/OrdalieTech/orb/bridge"
	"github.com/OrdalieTech/orb/bridge/vault"
)

// MitosisScope is the deterministic sync-scope id for a profile: every device on
// the same profile derives the same id, so they share one vault scope without any
// coordinated setup.
func MitosisScope(profile string) string {
	sum := sha256.Sum256([]byte("orb-mitosis-sync/" + profile))
	return base64.RawURLEncoding.EncodeToString(sum[:16])
}

// mitosisSync makes this device a member of the profile's sync scope, advertises its
// sealing key, optionally seals the supplied credentials to the scope's current
// members, and returns the vault presently sealed to this device. It is eventually
// consistent: each device must run it once to join, after which the reconcile loop
// gossips contacts and a re-run seals to everyone it has learned.
func (s *Service) mitosisSync(ctx context.Context, scope string, credentials []byte) (vault.Bundle, bool, error) {
	raw, err := s.Bridge.Admin(ctx, "status", bridge.JSON(struct{}{}))
	if err != nil {
		return vault.Bundle{}, false, err
	}
	var status struct {
		PeerID string   `json:"peer_id"`
		Peers  []string `json:"peers"`
	}
	if err := json.Unmarshal(raw, &status); err != nil {
		return vault.Bundle{}, false, err
	}
	members := append([]string{status.PeerID}, status.Peers...)
	if err := s.Bridge.SetScope(scope, members); err != nil {
		return vault.Bundle{}, false, err
	}
	// Advertise this device's sealing key first (with its locator when the transport
	// is up), so it is a member the vault can seal to.
	var endpoints []bridge.Locator
	if s.Peers != nil && s.Locator != nil {
		if locator, err := s.Locator(); err == nil && locator != "" {
			endpoints = []bridge.Locator{{Transport: "tailcat/1", Locator: locator}}
		}
	}
	if _, err := s.Bridge.PublishContact(scope, machineName(), endpoints, false); err != nil {
		return vault.Bundle{}, false, err
	}
	// Seal the credentials to the scope's current members (now including this device),
	// unless there is nothing worth sharing.
	if hasCredentials(credentials) {
		if err := vault.Publish(s.Bridge, scope, vault.Bundle{Credentials: credentials}, 1, uint64(time.Now().UnixNano())); err != nil {
			return vault.Bundle{}, false, err
		}
		// Re-publish the contact so it carries the freshly set vault.
		if _, err := s.Bridge.PublishContact(scope, machineName(), endpoints, false); err != nil {
			return vault.Bundle{}, false, err
		}
	}
	return s.Bridge.OpenVault(scope)
}

// hasCredentials reports whether an auth.json blob holds at least one credential.
func hasCredentials(data []byte) bool {
	creds, err := auth.ParseCredentials(data)
	return err == nil && len(creds) > 0
}
