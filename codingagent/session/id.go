package session

import (
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"sync"
	"time"
)

var uuidV7Clock = struct {
	sync.Mutex
	lastTimestamp int64
	sequence      uint32
}{lastTimestamp: -1 << 63}

func randomUUID() (string, error) {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", err
	}
	value[6] = value[6]&0x0f | 0x40
	value[8] = value[8]&0x3f | 0x80
	return formatUUID(value), nil
}

func randomUUIDv7(now time.Time) (string, error) {
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		return "", err
	}

	uuidV7Clock.Lock()
	milliseconds := now.UnixMilli()
	if milliseconds > uuidV7Clock.lastTimestamp {
		uuidV7Clock.lastTimestamp = milliseconds
		uuidV7Clock.sequence = binary.BigEndian.Uint32(random[6:10])
	} else {
		uuidV7Clock.sequence++
		if uuidV7Clock.sequence == 0 {
			uuidV7Clock.lastTimestamp++
		}
	}
	timestamp := uint64(uuidV7Clock.lastTimestamp)
	sequence := uuidV7Clock.sequence
	uuidV7Clock.Unlock()

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
	return formatUUID(value), nil
}

func randomEntryCandidate() (string, error) {
	var value [4]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(value[:]), nil
}

func formatUUID(value [16]byte) string {
	return fmt.Sprintf(
		"%08x-%04x-%04x-%04x-%012x",
		value[0:4], value[4:6], value[6:8], value[8:10], value[10:16],
	)
}
