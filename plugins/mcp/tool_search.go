package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/url"
	"reflect"
	"regexp"
	"slices"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/OrdalieTech/orb/agent/extensions"
	"github.com/OrdalieTech/orb/ai"
	"github.com/OrdalieTech/orb/engine"
	"github.com/OrdalieTech/orb/plugins/internal/toolutil"
	"golang.org/x/text/unicode/norm"
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
				_ = toolutil.Decode(args, &input)
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
	var documents []toolDocument
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

// toolDocument is a tool as the ranker sees it: its name, its description with its schema's descriptions and
// property names, and apart from them its namespace's name, description and instructions.
type toolDocument struct{ name, text, namespace string }

func searchDocument(tool extensions.ToolInfo) toolDocument {
	parts := []string{tool.Description}
	var schema any
	_ = json.Unmarshal(tool.Parameters, &schema)
	root, _ := schema.(map[string]any)
	parts = schemaText(root, root, parts, map[uintptr]bool{reflect.ValueOf(root).Pointer(): true})
	kept := parts[:0]
	for _, part := range parts {
		if strings.TrimSpace(part) != "" {
			kept = append(kept, part)
		}
	}
	document := toolDocument{name: tool.Name, text: strings.Join(kept, " ")}
	if tool.Namespace != nil {
		document.namespace = tool.Namespace.Name + " " + tool.Namespace.Description + " " + tool.Namespace.Instructions
	}
	return document
}

// schemaText collects a schema's descriptions and property names, following local $refs into root. seen
// holds the subschemas already reached through a reference, so recursive and shared definitions are read
// once, and definitions nothing references stay out.
func schemaText(schema, root any, parts []string, seen map[uintptr]bool) []string {
	object, ok := schema.(map[string]any)
	if !ok {
		return parts
	}
	if ref, ok := object["$ref"].(string); ok {
		if target := resolveSchemaRef(ref, root); target != nil && !seen[reflect.ValueOf(target).Pointer()] {
			seen[reflect.ValueOf(target).Pointer()] = true
			parts = schemaText(target, root, parts, seen)
		}
	}
	if description, ok := object["description"].(string); ok {
		parts = append(parts, description)
	}
	if properties, ok := object["properties"].(map[string]any); ok {
		for name, property := range properties {
			parts = schemaText(property, root, append(parts, name), seen)
		}
	}
	parts = schemaText(object["items"], root, parts, seen)
	for _, key := range []string{"anyOf", "oneOf", "allOf"} {
		if variants, ok := object[key].([]any); ok {
			for _, variant := range variants {
				parts = schemaText(variant, root, parts, seen)
			}
		}
	}
	return parts
}

// resolveSchemaRef resolves a local JSON pointer reference ("#", "#/$defs/name") in root; remote,
// missing and malformed references resolve to nil.
func resolveSchemaRef(ref string, root any) map[string]any {
	if ref != "#" && !strings.HasPrefix(ref, "#/") {
		return nil
	}
	current := root
	for _, segment := range strings.Split(ref[1:], "/")[1:] {
		if segment == "" {
			continue
		}
		decoded, err := url.PathUnescape(segment)
		object, ok := current.(map[string]any)
		if err != nil || !utf8.ValidString(decoded) || !ok {
			return nil
		}
		if current, ok = object[strings.ReplaceAll(strings.ReplaceAll(decoded, "~1", "/"), "~0", "~")]; !ok {
			return nil
		}
	}
	object, _ := current.(map[string]any)
	return object
}

var stopWords = map[string]bool{
	"a": true, "an": true, "and": true, "are": true, "as": true, "at": true, "be": true, "by": true, "for": true, "from": true, "in": true,
	"is": true, "it": true, "of": true, "on": true, "or": true, "that": true, "the": true, "this": true, "to": true, "with": true,
}

var (
	camelBoundary   = regexp.MustCompile(`([a-z0-9])([A-Z])`)
	acronymBoundary = regexp.MustCompile(`([A-Z]+)([A-Z][a-z])`)
	nonWord         = regexp.MustCompile(`[^\p{L}\p{M}\p{N}]+`)
	asciiWord       = regexp.MustCompile(`^[a-z0-9]+$`)
	pluralSuffix    = regexp.MustCompile(`(ches|shes|sses|xes|zes)$`)
	identifierChar  = regexp.MustCompile(`[^a-z0-9_$]`)
)

// tokenize splits NFC text at camelCase boundaries and between Unicode words, drops stop words and
// singularizes ASCII words naively.
//
// ponytail: words are runs of letters, marks and digits; upstream's Intl.Segmenter also splits text written
// without spaces (Chinese, Japanese, Thai) by dictionary, which the stdlib cannot.
func tokenize(text string) []string {
	text = acronymBoundary.ReplaceAllString(camelBoundary.ReplaceAllString(norm.NFC.String(text), "$1 $2"), "$1 $2")
	var terms []string
	for _, term := range strings.Fields(nonWord.ReplaceAllString(strings.ToLower(text), " ")) {
		if stopWords[term] {
			continue
		}
		if asciiWord.MatchString(term) {
			switch {
			case len(term) > 4 && strings.HasSuffix(term, "ies"):
				term = term[:len(term)-3] + "y"
			case len(term) > 4 && pluralSuffix.MatchString(term):
				term = term[:len(term)-2]
			case len(term) > 3 && strings.HasSuffix(term, "s") && !strings.HasSuffix(term, "ss"):
				term = term[:len(term)-1]
			}
		}
		terms = append(terms, term)
	}
	return terms
}

func termSet(text string) map[string]bool {
	set := map[string]bool{}
	for _, term := range tokenize(text) {
		set[term] = true
	}
	return set
}

// A query term in the name counts this many times its IDF, more than any number of mentions in the description
// (at most k1+1); one in the namespace, shared by every tool of a server, counts half.
const nameWeight, namespaceWeight = 3, 0.5

// rankBM25 returns the indexes of the best documents for query, best first: Okapi BM25 (k1 1.2, b 0.75) over
// the description, length-normalized, with the name and namespace as weighted fields. A query that is exactly a
// tool's name (or its part after a "__") ranks first, then tools matching more query terms; a tool whose whole
// local name is in the query gains one more match of every term. Ties keep document order (upstream #11331).
func rankBM25(query string, documents []toolDocument, limit int) []int {
	queryTerms := []string{}
	for _, term := range tokenize(query) {
		if !slices.Contains(queryTerms, term) {
			queryTerms = append(queryTerms, term)
		}
	}
	if len(queryTerms) == 0 || len(documents) == 0 || limit <= 0 {
		return nil
	}
	const k1, b = 1.2, 0.75
	type indexed struct {
		names, namespace map[string]bool
		local            map[string]bool
		counts           map[string]int
		length           float64
	}
	index := make([]indexed, len(documents))
	total := 0.0
	for position, document := range documents {
		segments := strings.Split(document.name, "__")
		entry := indexed{names: termSet(document.name), namespace: termSet(document.namespace),
			local: termSet(segments[len(segments)-1]), counts: map[string]int{}}
		for _, term := range tokenize(document.text) {
			entry.counts[term]++
			entry.length++
		}
		total += entry.length
		index[position] = entry
	}
	average := total / float64(len(documents))
	if average == 0 {
		average = 1
	}
	idf := map[string]float64{}
	fullNameBonus := 0.0
	for _, term := range queryTerms {
		frequency := 0
		for _, entry := range index {
			if entry.names[term] || entry.counts[term] > 0 || entry.namespace[term] {
				frequency++
			}
		}
		idf[term] = math.Log(1 + (float64(len(documents)-frequency)+0.5)/(float64(frequency)+0.5))
		fullNameBonus += idf[term]
	}
	exactQuery := identifierChar.ReplaceAllString(strings.ToLower(strings.TrimSpace(query)), "_")
	type match struct {
		index, covered int
		score          float64
		exact          bool
	}
	var matches []match
	for position, entry := range index {
		norm := k1 * (1 - b + b*entry.length/average)
		score, covered := 0.0, 0
		for _, term := range queryTerms {
			count := float64(entry.counts[term])
			weight := count * (k1 + 1) / (count + norm)
			if entry.names[term] {
				weight += nameWeight
			}
			if entry.namespace[term] {
				weight += namespaceWeight
			}
			if weight > 0 {
				covered++
			}
			score += idf[term] * weight
		}
		if score == 0 {
			continue
		}
		local := len(entry.local) > 0
		for term := range entry.local {
			local = local && slices.Contains(queryTerms, term)
		}
		if local {
			score += fullNameBonus
		}
		name := identifierChar.ReplaceAllString(strings.ToLower(strings.TrimSpace(documents[position].name)), "_")
		matches = append(matches, match{position, covered, score, name == exactQuery || strings.HasSuffix(name, "__"+exactQuery)})
	}
	sort.SliceStable(matches, func(i, j int) bool {
		if matches[i].exact != matches[j].exact {
			return matches[i].exact
		}
		if matches[i].covered != matches[j].covered {
			return matches[i].covered > matches[j].covered
		}
		return matches[i].score > matches[j].score
	})
	result := make([]int, 0, min(limit, len(matches)))
	for _, item := range matches[:min(limit, len(matches))] {
		result = append(result, item.index)
	}
	return result
}
