package tui

// killRing is the ring buffer behind Emacs-style kill/yank operations.
// Consecutive kills accumulate into one entry; prepend selects whether
// accumulated text goes before (backward deletion) or after (forward).
type killRing struct {
	ring []string
}

func (ring *killRing) push(text string, prepend, accumulate bool) {
	if text == "" {
		return
	}
	if accumulate && len(ring.ring) > 0 {
		last := ring.ring[len(ring.ring)-1]
		if prepend {
			ring.ring[len(ring.ring)-1] = text + last
		} else {
			ring.ring[len(ring.ring)-1] = last + text
		}
		return
	}
	ring.ring = append(ring.ring, text)
}

// peek returns the most recent entry, or "" when empty.
func (ring *killRing) peek() string {
	if len(ring.ring) == 0 {
		return ""
	}
	return ring.ring[len(ring.ring)-1]
}

// rotate moves the last entry to the front (yank-pop cycling).
func (ring *killRing) rotate() {
	if len(ring.ring) > 1 {
		last := ring.ring[len(ring.ring)-1]
		ring.ring = append([]string{last}, ring.ring[:len(ring.ring)-1]...)
	}
}

func (ring *killRing) length() int { return len(ring.ring) }
