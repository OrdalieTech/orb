package tools

import (
	"strings"

	"github.com/OrdalieTech/orb/internal/lazyregexp"
)

type BashResult struct {
	Output         string `json:"output"`
	ExitCode       *int   `json:"exitCode,omitempty"`
	Cancelled      bool   `json:"cancelled"`
	Truncated      bool   `json:"truncated"`
	FullOutputPath string `json:"fullOutputPath,omitempty"`
}

var rpcANSISequence = lazyregexp.New(`(?:\x1b\][\s\S]*?(?:\x07|\x1b\\|\x{009c}))|(?:(?:\x1b\[|\x{009b})[<=>]|[\x1b\x{009b}][[\]()#;?]*)(?:[0-9]{1,4}(?:[;:][0-9]{0,4})*)?[0-9A-PR-TZcf-nq-uy=><~]`)

func sanitizeBashOutput(value string) string {
	value = rpcANSISequence().ReplaceAllString(value, "")
	value = strings.ReplaceAll(value, "\r", "")
	return strings.Map(func(character rune) rune {
		switch {
		case character == '\t' || character == '\n':
			return character
		case character <= 0x1f:
			return -1
		case character >= 0xfff9 && character <= 0xfffb:
			return -1
		}
		return character
	}, value)
}
