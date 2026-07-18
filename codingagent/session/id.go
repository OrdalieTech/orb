package session

import (
	"crypto/rand"
	"fmt"
	"time"

	"github.com/OrdalieTech/pi-go/internal/uuidv7"
)

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
	return uuidv7.Generate(now)
}

func randomEntryCandidate() (string, error) {
	return uuidv7.EntryCandidate()
}

func formatUUID(value [16]byte) string {
	return fmt.Sprintf(
		"%08x-%04x-%04x-%04x-%012x",
		value[0:4], value[4:6], value[6:8], value[8:10], value[10:16],
	)
}
