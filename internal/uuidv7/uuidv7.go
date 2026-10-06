// Package uuidv7 generates the monotonic UUID and short entry identifiers used
// by upstream harness session storage, and crypto.randomUUID()'s UUIDv4.
package uuidv7

import (
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"io"
	mathrand "math/rand/v2"
	"sync"
	"time"
)

var clock = struct {
	sync.Mutex
	lastTimestamp int64
	sequence      uint32
}{lastTimestamp: -1 << 63}

// Generate returns a monotonic UUIDv7 for now.
func Generate(now time.Time) (string, error) {
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		return "", err
	}

	clock.Lock()
	milliseconds := now.UnixMilli()
	if milliseconds > clock.lastTimestamp {
		clock.lastTimestamp = milliseconds
		clock.sequence = binary.BigEndian.Uint32(random[6:10])
	} else {
		clock.sequence++
		if clock.sequence == 0 {
			clock.lastTimestamp++
		}
	}
	timestamp := uint64(clock.lastTimestamp)
	sequence := clock.sequence
	clock.Unlock()

	var value [16]byte
	var encoded [8]byte
	binary.BigEndian.PutUint64(encoded[:], timestamp)
	copy(value[:6], encoded[2:])
	value[6] = 0x70 | byte(sequence>>28)&0x0f
	value[7] = byte(sequence >> 20)
	value[8] = 0x80 | byte(sequence>>14)&0x3f
	value[9] = byte(sequence >> 6)
	value[10] = byte(sequence&0x3f)<<2 | random[10]&0x03
	copy(value[11:], random[11:])
	return format(value), nil
}

// EntryCandidate returns the eight-hex-character identifier used for session
// tree entries before collision checking. Callers check collisions, so it
// needs no crypto/rand, which on js/wasm calls into JavaScript every time.
func EntryCandidate() string {
	var value [4]byte
	binary.BigEndian.PutUint32(value[:], mathrand.Uint32())
	return hex.EncodeToString(value[:])
}

// NewV4 returns a lowercase-hex UUIDv4 read from random, as
// crypto.randomUUID() writes one.
func NewV4(random io.Reader) (string, error) {
	var value [16]byte
	if _, err := io.ReadFull(random, value[:]); err != nil {
		return "", err
	}
	value[6] = value[6]&0x0f | 0x40
	value[8] = value[8]&0x3f | 0x80
	return format(value), nil
}

func format(value [16]byte) string {
	return fmt.Sprintf(
		"%08x-%04x-%04x-%04x-%012x",
		value[0:4], value[4:6], value[6:8], value[8:10], value[10:16],
	)
}
