package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"regexp"
	"slices"
	"sort"
	"strings"

	"github.com/OrdalieTech/orb/agent/extensions"
	"github.com/OrdalieTech/orb/ai"
	"github.com/OrdalieTech/orb/engine"
)

// ToolSearchName is the tool that loads deferred tools, such as MCP servers'.
const ToolSearchName = "tool_search"

const defaultToolSearchLimit = 8

const toolSearchDescription = "# Tool discovery\n\nSearches over deferred tool metadata with BM25 and exposes matching tools for the next model call.\n\n" +
	"Some of the tools, such as tools of MCP servers, may not have been provided to you upfront, and you should use this tool (`tool_search`) to search for the required tools. For MCP tool discovery, always use `tool_search`."

// ToolSearchExtension registers tool_search inactive; the MCP integration
// activates it when servers have deferred tools. Loading a tool activates it,
// so the transcript records it and resume restores it.
func ToolSearchExtension() extensions.Factory {
	return func(api extensions.API) error {
		inactive := false
		api.RegisterTool(extensions.ToolDefinition{
			Name: ToolSearchName, Label: ToolSearchName, Description: toolSearchDescription,
			PromptSnippet: "Search for tools that are not loaded yet and load the matches",
			Parameters:    ai.JSONSchema(`{"type":"object","properties":{"query":{"type":"string","description":"Search query for deferred tools."},"limit":{"type":"number","description":"Maximum number of tools to return. Defaults to 8."}},"required":["query"]}`),
			Exposure:      extensions.ToolModelOnly, DefaultActive: &inactive,
			Execute: func(_ context.Context, _ string, args any, _ engine.AgentToolUpdateCallback, _ extensions.Context) (engine.AgentToolResult, error) {
				var input struct {
					Query string   `json:"query"`
					Limit *float64 `json:"limit"`
				}
				data, _ := json.Marshal(args)
				_ = json.Unmarshal(data, &input)
				if strings.TrimSpace(input.Query) == "" {
					return engine.AgentToolResult{}, errors.New("query must not be empty")
				}
				limit := defaultToolSearchLimit
				if input.Limit != nil {
					if *input.Limit <= 0 || *input.Limit != math.Trunc(*input.Limit) {
						return engine.AgentToolResult{}, errors.New("limit must be a positive integer")
					}
					limit = int(*input.Limit)
				}
				loaded, err := searchAndLoad(api, input.Query, limit)
				if err != nil {
					return engine.AgentToolResult{}, err
				}
				text := "No matching tools found."
				if len(loaded) > 0 {
					lines := make([]string, 0, len(loaded))
					for _, tool := range loaded {
						lines = append(lines, "- "+tool.Name+": "+firstLine(strings.TrimSpace(tool.Description)))
					}
					text = fmt.Sprintf("Loaded %d tool%s. They are available from your next call:\n%s", len(loaded), plural(len(loaded)), strings.Join(lines, "\n"))
				}
				names := make([]string, 0, len(loaded))
				for _, tool := range loaded {
					names = append(names, tool.Name)
				}
				return engine.AgentToolResult{Content: textToolContent(text), Details: map[string]any{"loaded": names}}, nil
			},
		})
		return nil
	}
}

// searchAndLoad ranks the deferred tools that are not active yet and
// activates the matches.
func searchAndLoad(api extensions.API, query string, limit int) ([]extensions.ToolInfo, error) {
	all, err := api.GetAllTools()
	if err != nil {
		return nil, err
	}
	active, err := api.GetActiveTools()
	if err != nil {
		return nil, err
	}
	var candidates []extensions.ToolInfo
	var documents []string
	for _, tool := range all {
		if tool.Exposure == extensions.ToolDeferred && !slices.Contains(active, tool.Name) {
			candidates = append(candidates, tool)
			documents = append(documents, searchDocument(tool))
		}
	}
	matches := rankBM25(query, documents, limit)
	loaded := make([]extensions.ToolInfo, 0, len(matches))
	for _, index := range matches {
		loaded = append(loaded, candidates[index])
		active = append(active, candidates[index].Name)
	}
	if len(loaded) > 0 {
		if err := api.SetActiveTools(active); err != nil {
			return nil, err
		}
	}
	return loaded, nil
}

// searchDocument is a tool's search text: its name, the name with _ as
// spaces, its description, its schema's descriptions and property names, and
// its namespace.
func searchDocument(tool extensions.ToolInfo) string {
	parts := []string{tool.Name, strings.ReplaceAll(tool.Name, "_", " "), tool.Description}
	var schema any
	_ = json.Unmarshal(tool.Parameters, &schema)
	parts = schemaText(schema, parts)
	if tool.Namespace != nil {
		parts = append(parts, tool.Namespace.Name, tool.Namespace.Description, tool.Namespace.Instructions)
	}
	kept := parts[:0]
	for _, part := range parts {
		if strings.TrimSpace(part) != "" {
			kept = append(kept, part)
		}
	}
	return strings.Join(kept, " ")
}

func schemaText(schema any, parts []string) []string {
	object, ok := schema.(map[string]any)
	if !ok {
		return parts
	}
	if description, ok := object["description"].(string); ok {
		parts = append(parts, description)
	}
	if properties, ok := object["properties"].(map[string]any); ok {
		for name, property := range properties {
			parts = schemaText(property, append(parts, name))
		}
	}
	parts = schemaText(object["items"], parts)
	for _, key := range []string{"anyOf", "oneOf", "allOf"} {
		if variants, ok := object[key].([]any); ok {
			for _, variant := range variants {
				parts = schemaText(variant, parts)
			}
		}
	}
	return parts
}

var stopWords = map[string]bool{
	"a": true, "an": true, "and": true, "are": true, "as": true, "at": true, "be": true, "by": true, "for": true, "from": true, "in": true,
	"is": true, "it": true, "of": true, "on": true, "or": true, "that": true, "the": true, "this": true, "to": true, "with": true,
}

var (
	camelBoundary   = regexp.MustCompile(`([a-z0-9])([A-Z])`)
	acronymBoundary = regexp.MustCompile(`([A-Z]+)([A-Z][a-z])`)
	nonAlphanumeric = regexp.MustCompile(`[^a-z0-9]+`)
	pluralSuffix    = regexp.MustCompile(`(ches|shes|sses|xes|zes)$`)
)

// tokenize lowercases text, splits it at camelCase boundaries and
// non-alphanumerics, drops stop words and singularizes naively.
func tokenize(text string) []string {
	text = acronymBoundary.ReplaceAllString(camelBoundary.ReplaceAllString(text, "$1 $2"), "$1 $2")
	var terms []string
	for _, term := range nonAlphanumeric.Split(strings.ToLower(text), -1) {
		if term == "" || stopWords[term] {
			continue
		}
		switch {
		case len(term) > 4 && strings.HasSuffix(term, "ies"):
			term = term[:len(term)-3] + "y"
		case len(term) > 4 && pluralSuffix.MatchString(term):
			term = term[:len(term)-2]
		case len(term) > 3 && strings.HasSuffix(term, "s") && !strings.HasSuffix(term, "ss"):
			term = term[:len(term)-1]
		}
		terms = append(terms, term)
	}
	return terms
}

// rankBM25 returns the indexes of the best documents for query, best first,
// with Okapi BM25 (k1 1.2, b 0.75); ties keep document order.
func rankBM25(query string, documents []string, limit int) []int {
	var queryTerms []string
	for _, term := range tokenize(query) {
		if !slices.Contains(queryTerms, term) {
			queryTerms = append(queryTerms, term)
		}
	}
	if len(queryTerms) == 0 || len(documents) == 0 || limit <= 0 {
		return nil
	}
	const k1, b = 1.2, 0.75
	counts := make([]map[string]int, len(documents))
	lengths := make([]float64, len(documents))
	total := 0.0
	for index, document := range documents {
		counts[index] = map[string]int{}
		for _, term := range tokenize(document) {
			counts[index][term]++
			lengths[index]++
		}
		total += lengths[index]
	}
	average := total / float64(len(documents))
	if average == 0 {
		average = 1
	}
	type match struct {
		index int
		score float64
	}
	var matches []match
	for index := range documents {
		score := 0.0
		for _, term := range queryTerms {
			count := counts[index][term]
			if count == 0 {
				continue
			}
			frequency := 0
			for _, other := range counts {
				if other[term] > 0 {
					frequency++
				}
			}
			idf := math.Log(1 + (float64(len(documents)-frequency)+0.5)/(float64(frequency)+0.5))
			norm := k1 * (1 - b + b*lengths[index]/average)
			score += idf * (float64(count) * (k1 + 1)) / (float64(count) + norm)
		}
		if score > 0 {
			matches = append(matches, match{index, score})
		}
	}
	sort.SliceStable(matches, func(i, j int) bool { return matches[i].score > matches[j].score })
	result := make([]int, 0, min(limit, len(matches)))
	for _, item := range matches[:min(limit, len(matches))] {
		result = append(result, item.index)
	}
	return result
}
