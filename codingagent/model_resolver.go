package codingagent

import (
	"fmt"
	"path"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/OrdalieTech/pi-go/ai"
	"github.com/OrdalieTech/pi-go/internal/localecompare"
)

const maxGlobBraceExpansions = 4096

var datedModelSuffix = regexp.MustCompile(`-\d{8}$`)

type ParsedModel struct {
	Model         *ai.Model
	ThinkingLevel *ai.ModelThinkingLevel
	Warning       string
}

type ScopedModel struct {
	Model         ai.Model
	ThinkingLevel *ai.ModelThinkingLevel
}

type ModelDiagnostic struct {
	Type, Message, Pattern string
}

// ParseModelPattern matches the complete id first so colons inside model ids remain literal.
func ParseModelPattern(pattern string, available []ai.Model, allowInvalidFallback ...bool) ParsedModel {
	if model := tryMatchModel(pattern, available); model != nil {
		return ParsedModel{Model: model}
	}
	colon := strings.LastIndexByte(pattern, ':')
	if colon < 0 {
		return ParsedModel{}
	}
	prefix, suffix := pattern[:colon], pattern[colon+1:]
	if level, valid := modelThinkingLevel(suffix); valid {
		result := ParseModelPattern(prefix, available, allowInvalidFallback...)
		if result.Model != nil && result.Warning == "" {
			result.ThinkingLevel = &level
		}
		return result
	}
	allow := true
	if len(allowInvalidFallback) > 0 {
		allow = allowInvalidFallback[0]
	}
	if !allow {
		return ParsedModel{}
	}
	result := ParseModelPattern(prefix, available, allowInvalidFallback...)
	if result.Model != nil {
		result.ThinkingLevel = nil
		result.Warning = `Invalid thinking level "` + suffix + `" in pattern "` + pattern + `". Using default instead.`
	}
	return result
}

func tryMatchModel(pattern string, available []ai.Model) *ai.Model {
	if model := exactModelReference(pattern, available); model != nil {
		return model
	}
	lower := strings.ToLower(pattern)
	matches := make([]ai.Model, 0)
	for _, model := range available {
		if strings.Contains(strings.ToLower(model.ID), lower) || strings.Contains(strings.ToLower(model.Name), lower) {
			matches = append(matches, model)
		}
	}
	if len(matches) == 0 {
		return nil
	}
	aliases := make([]ai.Model, 0, len(matches))
	dated := make([]ai.Model, 0, len(matches))
	for _, model := range matches {
		if strings.HasSuffix(model.ID, "-latest") || !datedModelSuffix.MatchString(model.ID) {
			aliases = append(aliases, model)
		} else {
			dated = append(dated, model)
		}
	}
	selected := aliases
	if len(selected) == 0 {
		selected = dated
	}
	collator := localecompare.New()
	slices.SortStableFunc(selected, func(left, right ai.Model) int {
		return collator.CompareString(right.ID, left.ID)
	})
	copy := selected[0]
	return &copy
}

func exactModelReference(reference string, available []ai.Model) *ai.Model {
	trimmed := strings.TrimSpace(reference)
	if trimmed == "" {
		return nil
	}
	lower := strings.ToLower(trimmed)
	matches := make([]ai.Model, 0, 1)
	for _, model := range available {
		if strings.ToLower(string(model.Provider)+"/"+model.ID) == lower {
			matches = append(matches, model)
		}
	}
	if len(matches) == 1 {
		copy := matches[0]
		return &copy
	}
	if len(matches) > 1 {
		return nil
	}
	if slash := strings.IndexByte(trimmed, '/'); slash >= 0 {
		provider := strings.TrimSpace(trimmed[:slash])
		modelID := strings.TrimSpace(trimmed[slash+1:])
		if provider != "" && modelID != "" {
			matches = matches[:0]
			for _, model := range available {
				if strings.EqualFold(string(model.Provider), provider) && strings.EqualFold(model.ID, modelID) {
					matches = append(matches, model)
				}
			}
			if len(matches) == 1 {
				copy := matches[0]
				return &copy
			}
			if len(matches) > 1 {
				return nil
			}
		}
	}
	matches = matches[:0]
	for _, model := range available {
		if strings.ToLower(model.ID) == lower {
			matches = append(matches, model)
		}
	}
	if len(matches) == 1 {
		copy := matches[0]
		return &copy
	}
	return nil
}

func ResolveModelScope(patterns []string, available []ai.Model) ([]ScopedModel, []ModelDiagnostic) {
	models := make([]ScopedModel, 0)
	diagnostics := make([]ModelDiagnostic, 0)
	for _, patternValue := range patterns {
		if strings.ContainsAny(patternValue, "*?[") {
			glob, level := splitGlobThinking(patternValue)
			matched := false
			for _, model := range available {
				full := strings.ToLower(string(model.Provider) + "/" + model.ID)
				id := strings.ToLower(model.ID)
				fullMatch := modelGlobMatch(strings.ToLower(glob), full)
				idMatch := modelGlobMatch(strings.ToLower(glob), id)
				if fullMatch || idMatch {
					matched = true
					if !containsScopedModel(models, model) {
						models = append(models, ScopedModel{Model: model, ThinkingLevel: level})
					}
				}
			}
			if !matched {
				diagnostics = append(diagnostics, noModelDiagnostic(patternValue))
			}
			continue
		}
		parsed := ParseModelPattern(patternValue, available)
		if parsed.Warning != "" {
			diagnostics = append(diagnostics, ModelDiagnostic{Type: "warning", Message: parsed.Warning, Pattern: patternValue})
		}
		if parsed.Model == nil {
			diagnostics = append(diagnostics, noModelDiagnostic(patternValue))
			continue
		}
		if !containsScopedModel(models, *parsed.Model) {
			models = append(models, ScopedModel{Model: *parsed.Model, ThinkingLevel: parsed.ThinkingLevel})
		}
	}
	return models, diagnostics
}

func modelGlobMatch(patternValue, modelID string) bool {
	patternValue = strings.ToLower(patternValue)
	modelID = strings.ToLower(modelID)
	negated := false
	for strings.HasPrefix(patternValue, "!") {
		negated = !negated
		patternValue = patternValue[1:]
	}
	if strings.HasPrefix(patternValue, "#") {
		return false
	}
	matched := false
	for _, expanded := range expandGlobBraces(patternValue) {
		if matchGlobPath(strings.Split(expanded, "/"), strings.Split(modelID, "/")) {
			matched = true
			break
		}
	}
	if negated {
		return !matched
	}
	return matched
}

func matchGlobPath(patterns, segments []string) bool {
	type state struct{ pattern, segment int }
	memo := make(map[state]bool)
	seen := make(map[state]bool)
	var match func(int, int) bool
	match = func(patternIndex, segmentIndex int) bool {
		current := state{patternIndex, segmentIndex}
		if seen[current] {
			return memo[current]
		}
		seen[current] = true
		if patternIndex == len(patterns) {
			memo[current] = segmentIndex == len(segments)
			return memo[current]
		}
		if patterns[patternIndex] == "**" {
			globstarAtStart := patternIndex == 0
			for patternIndex+1 < len(patterns) && patterns[patternIndex+1] == "**" {
				patternIndex++
			}
			if patternIndex+1 == len(patterns) {
				memo[current] = segmentIndex < len(segments) || globstarAtStart
				for index := segmentIndex; memo[current] && index < len(segments); index++ {
					memo[current] = !strings.HasPrefix(segments[index], ".")
				}
				return memo[current]
			}
			for next := segmentIndex; next <= len(segments); next++ {
				if next > segmentIndex && strings.HasPrefix(segments[next-1], ".") {
					break
				}
				if match(patternIndex+1, next) {
					memo[current] = true
					return memo[current]
				}
			}
			return memo[current]
		}
		if segmentIndex == len(segments) {
			return memo[current]
		}
		memo[current] = matchGlobSegment(patterns[patternIndex], segments[segmentIndex]) && match(patternIndex+1, segmentIndex+1)
		return memo[current]
	}
	return match(0, 0)
}

func matchGlobSegment(patternValue, value string) bool {
	pattern, text := []rune(patternValue), []rune(value)
	if len(text) > 0 && text[0] == '.' && !globCanStartDot(pattern) {
		return false
	}
	return slices.Contains(globEnds(pattern, text, 0), len(text))
}

func globGroupEnd(characters []rune, open int, opening, closing rune) int {
	depth := 0
	inClass := false
	for index := open; index < len(characters); index++ {
		if characters[index] == '\\' {
			index++
			continue
		}
		if opening != '[' && characters[index] == '[' {
			inClass = true
			continue
		}
		if characters[index] == ']' && inClass {
			inClass = false
			continue
		}
		if inClass {
			continue
		}
		switch characters[index] {
		case opening:
			depth++
		case closing:
			depth--
			if depth == 0 {
				return index
			}
		}
	}
	return -1
}

func splitGlobAlternatives(characters []rune) [][]rune {
	result := make([][]rune, 0, 1)
	start, depth := 0, 0
	inClass := false
	for index := 0; index < len(characters); index++ {
		if characters[index] == '\\' {
			index++
			continue
		}
		if characters[index] == '[' {
			inClass = true
			continue
		}
		if characters[index] == ']' && inClass {
			inClass = false
			continue
		}
		if inClass {
			continue
		}
		switch characters[index] {
		case '(':
			depth++
		case ')':
			depth--
		case '|':
			if depth == 0 {
				result = append(result, characters[start:index])
				start = index + 1
			}
		}
	}
	return append(result, characters[start:])
}

func globEnds(pattern, text []rune, start int) []int {
	if len(pattern) == 0 {
		return []int{start}
	}
	character, consumed := pattern[0], 1
	var positions []int
	if character == '\\' && len(pattern) > 1 {
		character, consumed = pattern[1], 2
		if start < len(text) && text[start] == character {
			positions = []int{start + 1}
		}
	} else if character == '[' {
		if close := globGroupEnd(pattern, 0, '[', ']'); close >= 0 {
			class := string(pattern[:close+1])
			if strings.HasPrefix(class, "[!") {
				class = "[^" + class[2:]
			}
			consumed = close + 1
			if start < len(text) {
				if matched, err := path.Match(class, string(text[start])); err == nil && matched {
					positions = []int{start + 1}
				}
			}
		} else if start < len(text) && text[start] == character {
			positions = []int{start + 1}
		}
	} else if strings.ContainsRune("@+?*!", character) && len(pattern) > 1 && pattern[1] == '(' {
		if close := globGroupEnd(pattern, 1, '(', ')'); close >= 0 {
			consumed = close + 1
			positions = globExtPositions(character, splitGlobAlternatives(pattern[2:close]), text, start)
		}
	} else {
		switch character {
		case '?':
			if start < len(text) {
				positions = []int{start + 1}
			}
		case '*':
			positions = make([]int, len(text)-start+1)
			for index := range positions {
				positions[index] = start + index
			}
		default:
			if start < len(text) && text[start] == character {
				positions = []int{start + 1}
			}
		}
	}
	result := make(map[int]struct{})
	for _, position := range positions {
		for _, end := range globEnds(pattern[consumed:], text, position) {
			result[end] = struct{}{}
		}
	}
	return globPositionKeys(result)
}

func globExtPositions(operator rune, alternatives [][]rune, text []rune, start int) []int {
	once := make(map[int]struct{})
	for _, alternative := range alternatives {
		for _, end := range globEnds(alternative, text, start) {
			once[end] = struct{}{}
		}
	}
	if operator == '@' {
		return globPositionKeys(once)
	}
	if operator == '?' {
		once[start] = struct{}{}
		return globPositionKeys(once)
	}
	if operator == '!' {
		result := make([]int, 0, len(text)-start+1)
		for end := start; end <= len(text); end++ {
			if _, excluded := once[end]; !excluded {
				result = append(result, end)
			}
		}
		return result
	}
	result, pending := make(map[int]struct{}), make([]int, 0, len(once)+1)
	if operator == '*' {
		result[start], pending = struct{}{}, append(pending, start)
	} else {
		for end := range once {
			result[end], pending = struct{}{}, append(pending, end)
		}
	}
	for len(pending) > 0 {
		position := pending[0]
		pending = pending[1:]
		for _, alternative := range alternatives {
			for _, end := range globEnds(alternative, text, position) {
				if _, exists := result[end]; !exists {
					result[end], pending = struct{}{}, append(pending, end)
				}
			}
		}
	}
	return globPositionKeys(result)
}

func globPositionKeys(positions map[int]struct{}) []int {
	result := make([]int, 0, len(positions))
	for position := range positions {
		result = append(result, position)
	}
	return result
}

func globCanStartDot(pattern []rune) bool {
	if len(pattern) == 0 {
		return false
	}
	if pattern[0] == '\\' && len(pattern) > 1 {
		return pattern[1] == '.'
	}
	if pattern[0] == '[' {
		if close := globGroupEnd(pattern, 0, '[', ']'); close >= 0 {
			class := string(pattern[:close+1])
			if strings.HasPrefix(class, "[!") {
				class = "[^" + class[2:]
			}
			matched, err := path.Match(class, ".")
			return err == nil && matched
		}
	}
	if strings.ContainsRune("@+?*!", pattern[0]) && len(pattern) > 1 && pattern[1] == '(' {
		if close := globGroupEnd(pattern, 1, '(', ')'); close >= 0 {
			for _, alternative := range splitGlobAlternatives(pattern[2:close]) {
				if globCanStartDot(append(append([]rune(nil), alternative...), pattern[close+1:]...)) {
					return true
				}
			}
			if strings.ContainsRune("?*!", pattern[0]) {
				return globCanStartDot(pattern[close+1:])
			}
		}
	}
	return pattern[0] == '.'
}

func expandGlobBraces(patternValue string) []string {
	start, end, alternatives, ok := nextBraceExpansion(patternValue)
	if !ok {
		return []string{patternValue}
	}
	result := make([]string, 0, len(alternatives))
	for _, alternative := range alternatives {
		//nolint:staticcheck // Incremental appends enforce the expansion cap without building the full subtree.
		for _, expanded := range expandGlobBraces(patternValue[:start] + alternative + patternValue[end+1:]) {
			result = append(result, expanded)
			if len(result) == maxGlobBraceExpansions {
				return result
			}
		}
	}
	return result
}

func nextBraceExpansion(patternValue string) (int, int, []string, bool) {
	stack := make([]int, 0)
	for index := 0; index < len(patternValue); index++ {
		if patternValue[index] == '\\' {
			index++
			continue
		}
		switch patternValue[index] {
		case '{':
			stack = append(stack, index)
		case '}':
			if len(stack) == 0 {
				continue
			}
			start := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			if alternatives, ok := braceAlternatives(patternValue[start+1 : index]); ok {
				return start, index, alternatives, true
			}
		}
	}
	return 0, 0, nil, false
}

func braceAlternatives(content string) ([]string, bool) {
	alternatives := make([]string, 0, 2)
	start, depth := 0, 0
	for index := 0; index < len(content); index++ {
		if content[index] == '\\' {
			index++
			continue
		}
		switch content[index] {
		case '{':
			depth++
		case '}':
			depth--
		case ',':
			if depth == 0 {
				alternatives = append(alternatives, content[start:index])
				start = index + 1
			}
		}
	}
	if len(alternatives) > 0 {
		return append(alternatives, content[start:]), true
	}
	return braceRange(content)
}

func braceRange(content string) ([]string, bool) {
	parts := strings.Split(content, "..")
	if len(parts) < 2 || len(parts) > 3 {
		return nil, false
	}
	if start, err := strconv.Atoi(parts[0]); err == nil {
		end, endErr := strconv.Atoi(parts[1])
		if endErr != nil {
			return nil, false
		}
		step := 1
		if end < start {
			step = -1
		}
		if len(parts) == 3 {
			parsed, stepErr := strconv.Atoi(parts[2])
			if stepErr != nil || parsed == 0 {
				return nil, false
			}
			if parsed < 0 {
				parsed = -parsed
			}
			step *= parsed
		}
		width := max(len(strings.TrimPrefix(parts[0], "-")), len(strings.TrimPrefix(parts[1], "-")))
		padded := len(strings.TrimPrefix(parts[0], "-")) > 1 && strings.HasPrefix(strings.TrimPrefix(parts[0], "-"), "0") ||
			len(strings.TrimPrefix(parts[1], "-")) > 1 && strings.HasPrefix(strings.TrimPrefix(parts[1], "-"), "0")
		result := make([]string, 0)
		for value := start; ; value += step {
			if step > 0 && value > end || step < 0 && value < end {
				break
			}
			if padded {
				if value < 0 {
					result = append(result, "-"+fmt.Sprintf("%0*d", width, -value))
				} else {
					result = append(result, fmt.Sprintf("%0*d", width, value))
				}
			} else {
				result = append(result, strconv.Itoa(value))
			}
		}
		return result, len(result) > 0
	}
	startRunes, endRunes := []rune(parts[0]), []rune(parts[1])
	if len(startRunes) != 1 || len(endRunes) != 1 {
		return nil, false
	}
	step := rune(1)
	if endRunes[0] < startRunes[0] {
		step = -1
	}
	if len(parts) == 3 {
		parsed, err := strconv.Atoi(parts[2])
		if err != nil || parsed == 0 {
			return nil, false
		}
		if parsed < 0 {
			parsed = -parsed
		}
		step *= rune(parsed)
	}
	result := make([]string, 0)
	for value := startRunes[0]; ; value += step {
		if step > 0 && value > endRunes[0] || step < 0 && value < endRunes[0] {
			break
		}
		result = append(result, string(value))
	}
	return result, len(result) > 0
}

func splitGlobThinking(patternValue string) (string, *ai.ModelThinkingLevel) {
	colon := strings.LastIndexByte(patternValue, ':')
	if colon >= 0 {
		if level, valid := modelThinkingLevel(patternValue[colon+1:]); valid {
			return patternValue[:colon], &level
		}
	}
	return patternValue, nil
}

func containsScopedModel(models []ScopedModel, target ai.Model) bool {
	return slices.ContainsFunc(models, func(model ScopedModel) bool {
		return model.Model.Provider == target.Provider && model.Model.ID == target.ID
	})
}

func noModelDiagnostic(patternValue string) ModelDiagnostic {
	return ModelDiagnostic{Type: "warning", Message: `No models match pattern "` + patternValue + `"`, Pattern: patternValue}
}

func modelThinkingLevel(value string) (ai.ModelThinkingLevel, bool) {
	level := ai.ModelThinkingLevel(value)
	switch level {
	case ai.ModelThinkingOff, ai.ModelThinkingMinimal, ai.ModelThinkingLow, ai.ModelThinkingMedium, ai.ModelThinkingHigh, ai.ModelThinkingXHigh, ai.ModelThinkingMax:
		return level, true
	default:
		return "", false
	}
}

type CLIModelResult struct {
	Model          *ai.Model
	ThinkingLevel  *ai.ModelThinkingLevel
	Warning, Error string
}

// ResolveCLIModel implements provider/id inference, fuzzy matching, and custom-id fallback.
func ResolveCLIModel(provider, pattern string, cliThinking *ai.ModelThinkingLevel, available []ai.Model, authChecks ...func(string) bool) CLIModelResult {
	if pattern == "" {
		return CLIModelResult{}
	}
	if len(available) == 0 {
		return CLIModelResult{Error: "No models available. Check your installation or add models to models.json."}
	}
	providers := make(map[string]string)
	for _, model := range available {
		providers[strings.ToLower(string(model.Provider))] = string(model.Provider)
	}
	explicitProvider := provider != ""
	canonicalProvider := providers[strings.ToLower(provider)]
	if explicitProvider && canonicalProvider == "" {
		return CLIModelResult{Error: `Unknown provider "` + provider + `". Use --list-models to see available providers/models.`}
	}
	modelPattern, inferred := pattern, false
	if canonicalProvider == "" {
		if prefix, rest, found := strings.Cut(pattern, "/"); found {
			if match := providers[strings.ToLower(prefix)]; match != "" {
				canonicalProvider, modelPattern, inferred = match, rest, true
			}
		}
	}
	if canonicalProvider == "" {
		lower := strings.ToLower(pattern)
		if model := slices.IndexFunc(available, func(candidate ai.Model) bool {
			return strings.ToLower(candidate.ID) == lower || strings.ToLower(string(candidate.Provider)+"/"+candidate.ID) == lower
		}); model >= 0 {
			copy := available[model]
			return CLIModelResult{Model: &copy}
		}
	}
	if explicitProvider && canonicalProvider != "" {
		prefix := canonicalProvider + "/"
		if len(pattern) >= len(prefix) && strings.EqualFold(pattern[:len(prefix)], prefix) {
			modelPattern = pattern[len(prefix):]
		}
	}
	candidates := available
	if canonicalProvider != "" {
		candidates = slices.DeleteFunc(append([]ai.Model(nil), available...), func(model ai.Model) bool { return string(model.Provider) != canonicalProvider })
	}
	parsed := ParseModelPattern(modelPattern, candidates, false)
	if parsed.Model != nil {
		if inferred && len(authChecks) > 0 && authChecks[0] != nil && !authChecks[0](string(parsed.Model.Provider)) {
			rawMatches := make([]ai.Model, 0, 1)
			for _, model := range available {
				if strings.EqualFold(model.ID, pattern) && (model.Provider != parsed.Model.Provider || model.ID != parsed.Model.ID) && authChecks[0](string(model.Provider)) {
					rawMatches = append(rawMatches, model)
				}
			}
			if len(rawMatches) == 1 {
				return CLIModelResult{Model: &rawMatches[0]}
			}
		}
		return CLIModelResult{Model: parsed.Model, ThinkingLevel: parsed.ThinkingLevel, Warning: parsed.Warning}
	}
	if inferred {
		lower := strings.ToLower(pattern)
		if index := slices.IndexFunc(available, func(model ai.Model) bool {
			return strings.ToLower(model.ID) == lower || strings.ToLower(string(model.Provider)+"/"+model.ID) == lower
		}); index >= 0 {
			copy := available[index]
			return CLIModelResult{Model: &copy}
		}
		full := ParseModelPattern(pattern, available, false)
		if full.Model != nil {
			return CLIModelResult{Model: full.Model, ThinkingLevel: full.ThinkingLevel, Warning: full.Warning}
		}
	}
	if canonicalProvider != "" && len(candidates) > 0 {
		id := modelPattern
		var thinking *ai.ModelThinkingLevel
		if cliThinking == nil {
			if colon := strings.LastIndexByte(id, ':'); colon >= 0 {
				if level, valid := modelThinkingLevel(id[colon+1:]); valid {
					id, thinking = id[:colon], &level
				}
			}
		}
		fallback := candidates[0]
		if defaultID := defaultModelPerProvider[canonicalProvider]; defaultID != "" {
			if index := slices.IndexFunc(candidates, func(model ai.Model) bool { return model.ID == defaultID }); index >= 0 {
				fallback = candidates[index]
			}
		}
		fallback.ID, fallback.Name = id, id
		requestedThinking := cliThinking
		if requestedThinking == nil {
			requestedThinking = thinking
		}
		if requestedThinking != nil && *requestedThinking != ai.ModelThinkingOff {
			fallback.Reasoning = true
		}
		return CLIModelResult{Model: &fallback, ThinkingLevel: thinking, Warning: `Model "` + id + `" not found for provider "` + canonicalProvider + `". Using custom model id.`}
	}
	display := pattern
	if canonicalProvider != "" {
		display = canonicalProvider + "/" + modelPattern
	}
	return CLIModelResult{Error: `Model "` + display + `" not found. Use --list-models to see available models.`}
}

var defaultModelPerProvider = map[string]string{
	"amazon-bedrock": "us.anthropic.claude-opus-4-6-v1", "ant-ling": "Ring-2.6-1T", "anthropic": "claude-opus-4-8",
	"openai": "gpt-5.5", "azure-openai-responses": "gpt-5.4", "openai-codex": "gpt-5.5", "nvidia": "nvidia/nemotron-3-super-120b-a12b",
	"deepseek": "deepseek-v4-pro", "google": "gemini-3.1-pro-preview", "google-vertex": "gemini-3.1-pro-preview", "github-copilot": "gpt-5.4",
	"openrouter": "moonshotai/kimi-k2.6", "vercel-ai-gateway": "zai/glm-5.1", "xai": "grok-4.5", "groq": "openai/gpt-oss-120b",
	"cerebras": "zai-glm-4.7", "zai": "glm-5.1", "zai-coding-cn": "glm-5.1", "mistral": "devstral-medium-latest",
	"minimax": "MiniMax-M2.7", "minimax-cn": "MiniMax-M2.7", "moonshotai": "kimi-k2.6", "moonshotai-cn": "kimi-k2.6",
	"huggingface": "moonshotai/Kimi-K2.6", "fireworks": "accounts/fireworks/models/kimi-k2p6", "together": "moonshotai/Kimi-K2.6",
	"opencode": "kimi-k2.6", "opencode-go": "kimi-k2.6", "kimi-coding": "kimi-for-coding", "cloudflare-workers-ai": "@cf/moonshotai/kimi-k2.6",
	"cloudflare-ai-gateway": "workers-ai/@cf/moonshotai/kimi-k2.6", "xiaomi": "mimo-v2.5-pro", "xiaomi-token-plan-cn": "mimo-v2.5-pro",
	"xiaomi-token-plan-ams": "mimo-v2.5-pro", "xiaomi-token-plan-sgp": "mimo-v2.5-pro",
}

var defaultModelProviderOrder = []string{
	"amazon-bedrock", "ant-ling", "anthropic", "openai", "azure-openai-responses", "openai-codex", "nvidia", "deepseek",
	"google", "google-vertex", "github-copilot", "openrouter", "vercel-ai-gateway", "xai", "groq", "cerebras", "zai",
	"zai-coding-cn", "mistral", "minimax", "minimax-cn", "moonshotai", "moonshotai-cn", "huggingface", "fireworks",
	"together", "opencode", "opencode-go", "kimi-coding", "cloudflare-workers-ai", "cloudflare-ai-gateway", "xiaomi",
	"xiaomi-token-plan-cn", "xiaomi-token-plan-ams", "xiaomi-token-plan-sgp",
}

// DefaultAvailableModel returns the provider's pinned upstream default only
// when that exact model is available after authentication.
func DefaultAvailableModel(provider string, available []ai.Model) *ai.Model {
	id := defaultModelPerProvider[provider]
	if id == "" {
		return nil
	}
	index := slices.IndexFunc(available, func(model ai.Model) bool {
		return string(model.Provider) == provider && model.ID == id
	})
	if index < 0 {
		return nil
	}
	copy := available[index]
	return &copy
}

// IsUnknownModel reports the Agent sentinel used when no model is selected.
func IsUnknownModel(model *ai.Model) bool {
	return model != nil && model.Provider == "unknown" && model.ID == "unknown" && model.API == "unknown"
}

func PreferredAvailableModel(available []ai.Model) *ai.Model {
	for _, provider := range defaultModelProviderOrder {
		id := defaultModelPerProvider[provider]
		if index := slices.IndexFunc(available, func(model ai.Model) bool {
			return string(model.Provider) == provider && model.ID == id
		}); index >= 0 {
			copy := available[index]
			return &copy
		}
	}
	if len(available) == 0 {
		return nil
	}
	copy := available[0]
	return &copy
}
