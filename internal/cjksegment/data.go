package cjksegment

import (
	_ "embed"
	"encoding/binary"
	"errors"
	"fmt"
)

const (
	dictionaryTrieUChars    = 1
	dictionaryTrieHasValues = 8
)

//go:embed data/cjdict.dict
var embeddedDictionary []byte

type dictionary struct {
	trie []byte
}

var cjkDictionary = mustDictionary(embeddedDictionary)

func mustDictionary(data []byte) dictionary {
	dictionary, err := parseDictionary(data)
	if err != nil {
		panic(err)
	}
	return dictionary
}

func parseDictionary(data []byte) (dictionary, error) {
	if len(data) < 32 {
		return dictionary{}, errors.New("cjk dictionary: truncated ICU data header")
	}
	headerSize := int(binary.BigEndian.Uint16(data[:2]))
	if headerSize < 24 || headerSize+32 > len(data) {
		return dictionary{}, errors.New("cjk dictionary: invalid ICU data header size")
	}
	if data[2] != 0xda || data[3] != 0x27 {
		return dictionary{}, errors.New("cjk dictionary: invalid ICU data magic")
	}
	if data[8] != 1 || data[10] != 2 {
		return dictionary{}, errors.New("cjk dictionary: unsupported byte order or UChar width")
	}
	if string(data[12:16]) != "Dict" || data[16] != 1 {
		return dictionary{}, errors.New("cjk dictionary: unsupported data format")
	}

	index := func(i int) int {
		start := headerSize + i*4
		return int(binary.BigEndian.Uint32(data[start : start+4]))
	}
	trieStart, trieEnd, totalSize := index(0), index(1), index(3)
	trieType := index(4)
	if trieStart < 32 || trieStart > trieEnd || trieEnd > totalSize || headerSize+totalSize > len(data) {
		return dictionary{}, errors.New("cjk dictionary: invalid section offsets")
	}
	if trieType != dictionaryTrieUChars|dictionaryTrieHasValues {
		return dictionary{}, fmt.Errorf("cjk dictionary: unsupported trie type %d", trieType)
	}
	if (trieEnd-trieStart)%2 != 0 {
		return dictionary{}, errors.New("cjk dictionary: unaligned UCharsTrie")
	}
	return dictionary{trie: data[headerSize+trieStart : headerSize+trieEnd]}, nil
}
