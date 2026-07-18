package cjksegment

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"reflect"
	"testing"
)

func TestEmbeddedDictionaryProvenance(t *testing.T) {
	if got := len(embeddedDictionary); got != 2_007_296 {
		t.Fatalf("dictionary size = %d, want 2007296", got)
	}
	sum := sha256.Sum256(embeddedDictionary)
	if got, want := hex.EncodeToString(sum[:]), "5b96312a434f4ca3df1f5fa906e88d52fe2e28e3b87c68b9e62d0d77e1995edc"; got != want {
		t.Fatalf("dictionary SHA-256 = %s, want %s", got, want)
	}
	if _, err := parseDictionary(embeddedDictionary); err != nil {
		t.Fatalf("parse dictionary: %v", err)
	}
}

func TestParseDictionaryRejectsInvalidData(t *testing.T) {
	for name, mutate := range map[string]func([]byte){
		"magic":  func(data []byte) { data[2] = 0 },
		"format": func(data []byte) { data[12] = 'X' },
		"type":   func(data []byte) { data[163] = 1 },
	} {
		t.Run(name, func(t *testing.T) {
			data := append([]byte(nil), embeddedDictionary...)
			mutate(data)
			if _, err := parseDictionary(data); err == nil {
				t.Fatal("parseDictionary accepted invalid data")
			}
		})
	}
}

func TestSplitMatchesICU78(t *testing.T) {
	cases := []struct {
		input string
		want  []string
	}{
		{"你好世界", []string{"你好", "世界"}},
		{"中华人民共和国", []string{"中华", "人民", "共和国"}},
		{"東京都に行く", []string{"東京", "都", "に", "行く"}},
		{"カタカナテスト", []string{"カタカナ", "テスト"}},
		{"ｶﾞｸｾｲ", []string{"ｶﾞｸ", "ｾｲ"}},
		{"𠀀世界", []string{"𠀀", "世界"}},
		{"神世界", []string{"神", "世界"}},
	}
	for _, testCase := range cases {
		t.Run(testCase.input, func(t *testing.T) {
			if got := Split(testCase.input); !reflect.DeepEqual(got, testCase.want) {
				t.Fatalf("Split(%q) = %q, want %q", testCase.input, got, testCase.want)
			}
		})
	}
}

func TestDictionaryScriptMembershipMatchesICU78(t *testing.T) {
	for _, value := range []rune{'你', 'あ', 'カ', '\u30FC', '\uFF70', '\uFF9E', 0x33479} {
		if !IsDictionaryRune(value) {
			t.Errorf("U+%04X should be in the CJK dictionary set", value)
		}
	}
	for _, value := range []rune{'A', '가', '\u30A0', '\u30FB', '\u32FF', 0x3347A} {
		if IsDictionaryRune(value) {
			t.Errorf("U+%04X should not be in the CJK dictionary set", value)
		}
	}
}

func TestDictionaryScriptSetMatchesNodeICU78(t *testing.T) {
	hash := sha256.New()
	var encoded [4]byte
	count := 0
	for value := rune(0); value <= '\U0010FFFF'; value++ {
		if !IsDictionaryRune(value) {
			continue
		}
		binary.BigEndian.PutUint32(encoded[:], uint32(value))
		_, _ = hash.Write(encoded[:])
		count++
	}
	if count != 104_057 {
		t.Fatalf("dictionary-script set size = %d, want 104057", count)
	}
	if got, want := hex.EncodeToString(hash.Sum(nil)), "9a84f9514a9d735583f1b88b8c07e43ece27feaeba6cc3fbda3baba5f2c4c149"; got != want {
		t.Fatalf("dictionary-script set SHA-256 = %s, want %s", got, want)
	}
}
