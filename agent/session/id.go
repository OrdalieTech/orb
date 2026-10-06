package session

import (
	"github.com/OrdalieTech/orb/internal/uuidv7"
)

func randomEntryCandidate() (string, error) {
	return uuidv7.EntryCandidate(), nil
}
