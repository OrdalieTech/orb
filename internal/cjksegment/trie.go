package cjksegment

import "unicode/utf16"

type trieResult uint8

const (
	trieNoMatch trieResult = iota
	trieNoValue
	trieFinalValue
	trieIntermediateValue
)

const (
	maxBranchLinearSubNodeLength = 5
	minLinearMatch               = 0x30
	minValueLead                 = 0x40
	nodeTypeMask                 = 0x3f
	valueIsFinal                 = 0x8000
	minTwoUnitValueLead          = 0x4000
	threeUnitValueLead           = 0x7fff
	minTwoUnitNodeValueLead      = 0x4040
	threeUnitNodeValueLead       = 0x7fc0
	minTwoUnitDeltaLead          = 0xfc00
	threeUnitDeltaLead           = 0xffff
)

func (result trieResult) hasValue() bool { return result >= trieFinalValue }
func (result trieResult) hasNext() bool  { return result&1 != 0 }

func resultForValueNode(node uint16) trieResult {
	if node&valueIsFinal != 0 {
		return trieFinalValue
	}
	return trieIntermediateValue
}

// charsTrie is the read-only subset of ICU UCharsTrie 1.0 used by cjdict.
type charsTrie struct {
	data                 []byte
	pos                  int
	remainingMatchLength int
}

func newCharsTrie(data []byte) charsTrie {
	return charsTrie{data: data, remainingMatchLength: -1}
}

func (trie *charsTrie) unit(pos int) uint16 {
	pos *= 2
	return uint16(trie.data[pos])<<8 | uint16(trie.data[pos+1])
}

func (trie *charsTrie) stop() {
	trie.pos = -1
}

func (trie *charsTrie) firstRune(value rune) trieResult {
	trie.remainingMatchLength = -1
	if value <= 0xffff {
		return trie.nextImpl(0, uint16(value))
	}
	lead, trail := utf16.EncodeRune(value)
	result := trie.nextImpl(0, uint16(lead))
	if !result.hasNext() {
		trie.stop()
		return trieNoMatch
	}
	return trie.nextUnit(uint16(trail))
}

func (trie *charsTrie) nextRune(value rune) trieResult {
	if value <= 0xffff {
		return trie.nextUnit(uint16(value))
	}
	lead, trail := utf16.EncodeRune(value)
	result := trie.nextUnit(uint16(lead))
	if !result.hasNext() {
		trie.stop()
		return trieNoMatch
	}
	return trie.nextUnit(uint16(trail))
}

func (trie *charsTrie) nextUnit(input uint16) trieResult {
	pos := trie.pos
	if pos < 0 {
		return trieNoMatch
	}
	length := trie.remainingMatchLength
	if length >= 0 {
		if input != trie.unit(pos) {
			trie.stop()
			return trieNoMatch
		}
		pos++
		length--
		trie.remainingMatchLength = length
		trie.pos = pos
		if length < 0 {
			node := trie.unit(pos)
			if node >= minValueLead {
				return resultForValueNode(node)
			}
		}
		return trieNoValue
	}
	return trie.nextImpl(pos, input)
}

func (trie *charsTrie) nextImpl(pos int, input uint16) trieResult {
	node := trie.unit(pos)
	pos++
	for {
		switch {
		case node < minLinearMatch:
			return trie.branchNext(pos, int(node), input)
		case node < minValueLead:
			length := int(node - minLinearMatch)
			if input != trie.unit(pos) {
				trie.stop()
				return trieNoMatch
			}
			pos++
			length--
			trie.remainingMatchLength = length
			trie.pos = pos
			if length < 0 {
				next := trie.unit(pos)
				if next >= minValueLead {
					return resultForValueNode(next)
				}
			}
			return trieNoValue
		case node&valueIsFinal != 0:
			trie.stop()
			return trieNoMatch
		default:
			pos = trie.skipNodeValue(pos, node)
			node &= nodeTypeMask
		}
	}
}

func (trie *charsTrie) branchNext(pos, length int, input uint16) trieResult {
	if length == 0 {
		length = int(trie.unit(pos))
		pos++
	}
	length++
	for length > maxBranchLinearSubNodeLength {
		comparison := trie.unit(pos)
		pos++
		if input < comparison {
			length >>= 1
			pos = trie.jumpByDelta(pos)
		} else {
			length -= length >> 1
			pos = trie.skipDelta(pos)
		}
	}
	for length > 1 {
		if input == trie.unit(pos) {
			pos++
			node := trie.unit(pos)
			var result trieResult
			if node&valueIsFinal != 0 {
				result = trieFinalValue
			} else {
				pos++
				delta, nextPos := trie.readValue(pos, node)
				pos = nextPos + delta
				node = trie.unit(pos)
				if node >= minValueLead {
					result = resultForValueNode(node)
				} else {
					result = trieNoValue
				}
			}
			trie.pos = pos
			trie.remainingMatchLength = -1
			return result
		}
		pos++
		length--
		pos = trie.skipValueAt(pos)
	}
	if input != trie.unit(pos) {
		trie.stop()
		return trieNoMatch
	}
	pos++
	trie.pos = pos
	trie.remainingMatchLength = -1
	node := trie.unit(pos)
	if node >= minValueLead {
		return resultForValueNode(node)
	}
	return trieNoValue
}

func (trie *charsTrie) value() int {
	lead := trie.unit(trie.pos)
	if lead&valueIsFinal != 0 {
		value, _ := trie.readValue(trie.pos+1, lead&^valueIsFinal)
		return value
	}
	return trie.readNodeValue(trie.pos+1, lead)
}

func (trie *charsTrie) readValue(pos int, lead uint16) (int, int) {
	switch {
	case lead < minTwoUnitValueLead:
		return int(lead), pos
	case lead < threeUnitValueLead:
		return int(lead-minTwoUnitValueLead)<<16 | int(trie.unit(pos)), pos + 1
	default:
		return int(trie.unit(pos))<<16 | int(trie.unit(pos+1)), pos + 2
	}
}

func (trie *charsTrie) skipValueAt(pos int) int {
	lead := trie.unit(pos) &^ valueIsFinal
	pos++
	if lead >= minTwoUnitValueLead {
		if lead < threeUnitValueLead {
			pos++
		} else {
			pos += 2
		}
	}
	return pos
}

func (trie *charsTrie) readNodeValue(pos int, lead uint16) int {
	switch {
	case lead < minTwoUnitNodeValueLead:
		return int(lead>>6) - 1
	case lead < threeUnitNodeValueLead:
		return int((lead&0x7fc0)-minTwoUnitNodeValueLead)<<10 | int(trie.unit(pos))
	default:
		return int(trie.unit(pos))<<16 | int(trie.unit(pos+1))
	}
}

func (trie *charsTrie) skipNodeValue(pos int, lead uint16) int {
	if lead >= minTwoUnitNodeValueLead {
		if lead < threeUnitNodeValueLead {
			pos++
		} else {
			pos += 2
		}
	}
	return pos
}

func (trie *charsTrie) jumpByDelta(pos int) int {
	delta := int(trie.unit(pos))
	pos++
	if delta >= minTwoUnitDeltaLead {
		if delta == threeUnitDeltaLead {
			delta = int(trie.unit(pos))<<16 | int(trie.unit(pos+1))
			pos += 2
		} else {
			delta = (delta-minTwoUnitDeltaLead)<<16 | int(trie.unit(pos))
			pos++
		}
	}
	return pos + delta
}

func (trie *charsTrie) skipDelta(pos int) int {
	delta := trie.unit(pos)
	pos++
	if delta >= minTwoUnitDeltaLead {
		if delta == threeUnitDeltaLead {
			pos += 2
		} else {
			pos++
		}
	}
	return pos
}
