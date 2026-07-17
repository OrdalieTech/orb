package faux

import (
	"math/rand/v2"
	"unicode/utf8"
)

type utf16Chunk struct {
	units []uint16
	text  string
}

func splitUTF16ByTokenSize(text string, minTokenSize, maxTokenSize int) []utf16Chunk {
	units := utf16Units(text)
	chunks := make([]utf16Chunk, 0)
	for index := 0; index < len(units); {
		tokenSize := minTokenSize
		if difference := maxTokenSize - minTokenSize; difference > 0 {
			tokenSize += rand.IntN(difference + 1)
		}
		charSize := max(1, tokenSize*4)
		end := min(len(units), index+charSize)
		part := append([]uint16(nil), units[index:end]...)
		chunks = append(chunks, utf16Chunk{units: part, text: stringFromUTF16(part)})
		index = end
	}
	if len(chunks) == 0 {
		chunks = append(chunks, utf16Chunk{text: ""})
	}
	return chunks
}

func utf16Units(value string) []uint16 {
	units := make([]uint16, 0, len(value))
	for index := 0; index < len(value); {
		if surrogate, ok := decodeWTF8Surrogate(value[index:]); ok {
			units = append(units, surrogate)
			index += 3
			continue
		}
		runeValue, size := utf8.DecodeRuneInString(value[index:])
		if runeValue == utf8.RuneError && size == 1 {
			units = append(units, uint16(utf8.RuneError))
			index++
			continue
		}
		index += size
		if runeValue <= 0xffff {
			units = append(units, uint16(runeValue))
			continue
		}
		value := uint32(runeValue) - 0x10000
		units = append(units, uint16(0xd800+(value>>10)), uint16(0xdc00+(value&0x3ff)))
	}
	return units
}

func stringFromUTF16(units []uint16) string {
	result := make([]byte, 0, len(units)*3)
	for index := 0; index < len(units); index++ {
		unit := units[index]
		if unit >= 0xd800 && unit <= 0xdbff && index+1 < len(units) {
			low := units[index+1]
			if low >= 0xdc00 && low <= 0xdfff {
				runeValue := rune(0x10000 + (uint32(unit-0xd800) << 10) + uint32(low-0xdc00))
				result = utf8.AppendRune(result, runeValue)
				index++
				continue
			}
		}
		if unit >= 0xd800 && unit <= 0xdfff {
			result = append(result,
				byte(0xe0|unit>>12),
				byte(0x80|(unit>>6)&0x3f),
				byte(0x80|unit&0x3f),
			)
			continue
		}
		result = utf8.AppendRune(result, rune(unit))
	}
	return string(result)
}

func decodeWTF8Surrogate(value string) (uint16, bool) {
	if len(value) < 3 || value[0] != 0xed || value[1] < 0xa0 || value[1] > 0xbf || value[2] < 0x80 || value[2] > 0xbf {
		return 0, false
	}
	unit := uint16(value[0]&0x0f)<<12 | uint16(value[1]&0x3f)<<6 | uint16(value[2]&0x3f)
	return unit, unit >= 0xd800 && unit <= 0xdfff
}
