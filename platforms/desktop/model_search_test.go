package main

import (
	"slices"
	"testing"
)

func TestFuzzyFilterModels(t *testing.T) {
	models := []string{
		"anthropic/claude-opus-5-5",
		"anthropic/claude-sonnet-4-6",
		"anthropic/claude-haiku-4-5",
		"openai/gpt-5.4",
		"openai/gpt-5.4-mini",
		"openai-codex/gpt-6.2-sol",
		"google/gemini-4-neon",
		"google/gemini-2.5-pro",
		"deepseek/deepseek-r1",
	}

	tests := []struct {
		name      string
		query     string
		wantTop   string
		wantIn    []string
		wantNotIn []string
	}{
		{
			name:    "empty query returns all",
			query:   "   ",
			wantTop: "anthropic/claude-opus-5-5",
			wantIn:  models,
		},
		{
			name:    "separator-agnostic version 54 matches 5.4",
			query:   "54",
			wantTop: "openai/gpt-5.4",
			wantIn:  []string{"openai/gpt-5.4", "openai/gpt-5.4-mini"},
		},
		{
			name:    "multi-token with dot matches dash",
			query:   "opus 5.5",
			wantTop: "anthropic/claude-opus-5-5",
		},
		{
			name:    "collapsed token opus55 matches claude-opus-5-5",
			query:   "opus55",
			wantTop: "anthropic/claude-opus-5-5",
		},
		{
			name:    "multi-token any order",
			query:   "62 codex",
			wantTop: "openai-codex/gpt-6.2-sol",
		},
		{
			name:    "alias gem with multi-token",
			query:   "gem 4 n",
			wantTop: "google/gemini-4-neon",
		},
		{
			name:    "subsequence g4n matches gemini-4-neon",
			query:   "g4n",
			wantTop: "google/gemini-4-neon",
		},
		{
			name:    "subsequence cs46 matches claude-sonnet-4-6",
			query:   "cs46",
			wantTop: "anthropic/claude-sonnet-4-6",
		},
		{
			name:      "alias oai matches openai",
			query:     "oai 54",
			wantTop:   "openai/gpt-5.4",
			wantNotIn: []string{"anthropic/claude-opus-5-5"},
		},
		{
			name:      "alias anth matches anthropic",
			query:     "anth haiku",
			wantTop:   "anthropic/claude-haiku-4-5",
			wantNotIn: []string{"openai/gpt-5.4"},
		},
		{
			name:    "alias ds matches deepseek",
			query:   "ds r1",
			wantTop: "deepseek/deepseek-r1",
		},
		{
			name:    "exact short name beats longer prefix",
			query:   "gpt-5.4",
			wantTop: "openai/gpt-5.4",
			wantIn:  []string{"openai/gpt-5.4", "openai/gpt-5.4-mini"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := fuzzyFilterModels(models, tc.query)
			if len(got) == 0 {
				t.Fatalf("fuzzyFilterModels(%q) returned empty, want top %q", tc.query, tc.wantTop)
			}
			if got[0] != tc.wantTop {
				t.Errorf("fuzzyFilterModels(%q) top = %q, want %q (all: %v)", tc.query, got[0], tc.wantTop, got)
			}
			for _, w := range tc.wantIn {
				if !slices.Contains(got, w) {
					t.Errorf("fuzzyFilterModels(%q) missing %q in %v", tc.query, w, got)
				}
			}
			for _, nw := range tc.wantNotIn {
				if slices.Contains(got, nw) {
					t.Errorf("fuzzyFilterModels(%q) unexpectedly included %q in %v", tc.query, nw, got)
				}
			}
		})
	}
}

func TestFuzzyFilterModelsNoMatch(t *testing.T) {
	models := []string{"anthropic/claude-sonnet-4-6", "openai/gpt-5.4"}
	if got := fuzzyFilterModels(models, "zzzznotamodel"); len(got) != 0 {
		t.Fatalf("expected no matches, got %v", got)
	}
}
