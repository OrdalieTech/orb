package qr

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"
)

// Golden matrices, checked bit for bit against the python qrcode library at the same mask.
func TestEncodeMatchesReferenceMatrices(t *testing.T) {
	for _, tc := range []struct {
		n, size int
		sum     string
	}{
		{1, 21, "a4dece7f45d05a09"},
		{30, 25, "c63e96e54dc8cda2"},
		{400, 69, "fa7a1cf5b92699d1"},
		{700, 89, "c5bd8b02e672bb56"},
		{2900, 177, "eb6c9f5c442d80da"},
	} {
		code, err := Encode([]byte(strings.Repeat("orb-bridge:v1:", tc.n/14+1)[:tc.n]))
		if err != nil {
			t.Fatal(err)
		}
		rows := make([]string, len(code))
		for r, row := range code {
			for _, dark := range row {
				rows[r] += map[bool]string{true: "1", false: "0"}[dark]
			}
		}
		sum := sha256.Sum256([]byte(strings.Join(rows, "\n")))
		if len(code) != tc.size || hex.EncodeToString(sum[:8]) != tc.sum {
			t.Errorf("%d bytes: size %d sum %x, want %d %s", tc.n, len(code), sum[:8], tc.size, tc.sum)
		}
	}
}

func TestEncodeRejectsWhatNoVersionHolds(t *testing.T) {
	if _, err := Encode(make([]byte, 2953)); err != nil {
		t.Fatalf("2953 bytes fit version 40-L: %v", err)
	}
	if _, err := Encode(make([]byte, 2954)); err == nil {
		t.Fatal("2954 bytes accepted")
	}
}

func TestTerminalDrawsTwoModulesPerCell(t *testing.T) {
	code, _ := Encode([]byte("orb"))
	lines := strings.Split(strings.TrimSuffix(code.Terminal(), "\n"), "\n")
	if want := (21 + 2 + 1) / 2; len(lines) != want {
		t.Fatalf("%d lines, want %d", len(lines), want)
	}
	if got := strings.Count(lines[0], "▀"); got != 23 {
		t.Fatalf("%d cells per line, want 23", got)
	}
}
