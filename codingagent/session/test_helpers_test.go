package session

import (
	"fmt"
	"testing"
	"time"
)

func fixedTestTime(t *testing.T) time.Time {
	t.Helper()
	value, err := time.Parse(time.RFC3339Nano, "2025-01-02T03:04:05.006Z")
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func sequenceIDGenerator(ids ...string) IDGenerator {
	index := 0
	return func() (string, error) {
		if index >= len(ids) {
			return "", fmt.Errorf("test id sequence exhausted at %d", index)
		}
		id := ids[index]
		index++
		return id, nil
	}
}

func failingIDGenerator(message string) IDGenerator {
	return func() (string, error) { return "", fmt.Errorf("%s", message) }
}
