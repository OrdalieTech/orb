// Package uuidv7 generates the monotonic UUID and short entry identifiers used
// by upstream harness session storage.
package uuidv7

import (
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"fmt"
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
// tree entries before collision checking.
func EntryCandidate() (string, error) {
	var value [4]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(value[:]), nil
}

func format(value [16]byte) string {
	return fmt.Sprintf(
		"%08x-%04x-%04x-%04x-%012x",
		value[0:4], value[4:6], value[6:8], value[8:10], value[10:16],
	)
}
