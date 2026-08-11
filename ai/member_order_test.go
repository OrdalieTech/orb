package ai

import (
	"testing"
)

var memberOrderCorpus = []string{
	``,
	`{}`,
	`null`,
	`[1,2]`,
	`"errorMessage"`,
	`{"errorMessage":"x","timestamp":1}`,
	`{"timestamp":1,"errorMessage":"x"}`,
	`{"errorMessage":"x","responseId":"r","timestamp":1}`,
	`{"timestamp":1,"errorMessage":"x","responseId":"r"}`,
	`{"responseId":"r","timestamp":1}`,
	`  {  "errorMessage" : "x" , "timestamp" : 1 }  `,
	`{"errorMessage":"x","timestamp":1}`,
	`{"timestamp":1,"errorMessage":"x"}`,
	`{"nested":{"errorMessage":"inner","timestamp":2},"timestamp":1,"errorMessage":"x"}`,
	`{"list":[{"timestamp":9}],"errorMessage":"x","timestamp":1}`,
	`{"errorMessage":"quote \" and \\ backslash","timestamp":1}`,
	`{"a":"errorMessage","timestamp":1,"errorMessage":"x"}`,
	`{"errorMessage":"x","errorMessage":"y","timestamp":1,"timestamp":2}`,
	`{"timestamp":1,"timestamp":2,"errorMessage":"x"}`,
	`{"unicode":"héllo ☃","errorMessage":"x","timestamp":1}`,
	`{"errorMessage":`,
	`{"errorMessage":"x","timestamp":}`,
	`{"errorMessage" "x"}`,
	`{"errorMessage":"x"} trailing`,
	`{"errorMessage":"x","timestamp":1}{"again":true}`,
	`{"cacheWrite1h":1,"totalTokens":2}`,
	`{"totalTokens":2,"reasoning":1}`,
	`{"reasoning":1,"cacheWrite1h":2,"totalTokens":3}`,
	`{"a":{"b":[{"c":"}"}]},"errorMessage":"x","timestamp":1}`,
	`{"":"","errorMessage":"x","timestamp":1}`,
	"{\"errorMessage\":\"x\",\n\"timestamp\":1}",
	`{"responseId":"r","errorMessage":"x"}`,
	`{"errorMessage":"x","responseId":"r"}`,
	`{"错误":"x","errorMessage":"y","timestamp":1}`,
}

var memberOrderPairs = [][2]string{
	{"errorMessage", "timestamp"},
	{"errorMessage", "responseId"},
	{"cacheWrite1h", "totalTokens"},
	{"reasoning", "totalTokens"},
	{"timestamp", "errorMessage"},
	{"a", "b"},
}

func checkMemberOrderAgainstDecoder(t *testing.T, data []byte) {
	t.Helper()
	for _, pair := range memberOrderPairs {
		got := topLevelMemberBefore(data, pair[0], pair[1])
		want := topLevelMemberBeforeDecoder(data, pair[0], pair[1])
		if got != want {
			t.Errorf("topLevelMemberBefore(%q, %q, %q) = %v, decoder = %v", data, pair[0], pair[1], got, want)
		}
	}
	gotTimestamp, gotResponseID := assistantErrorMemberOrder(data)
	wantTimestamp := topLevelMemberBeforeDecoder(data, "errorMessage", "timestamp")
	wantResponseID := !wantTimestamp && topLevelMemberBeforeDecoder(data, "errorMessage", "responseId")
	if gotTimestamp != wantTimestamp || gotResponseID != wantResponseID {
		t.Errorf("assistantErrorMemberOrder(%q) = (%v, %v), decoder pair = (%v, %v)",
			data, gotTimestamp, gotResponseID, wantTimestamp, wantResponseID)
	}
}

func TestTopLevelMemberBeforeMatchesDecoder(t *testing.T) {
	for _, input := range memberOrderCorpus {
		checkMemberOrderAgainstDecoder(t, []byte(input))
	}
}

func FuzzTopLevelMemberBefore(f *testing.F) {
	for _, input := range memberOrderCorpus {
		f.Add([]byte(input))
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		checkMemberOrderAgainstDecoder(t, data)
	})
}
