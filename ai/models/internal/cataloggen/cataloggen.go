// Package cataloggen builds the pi model catalog from the models.dev v2
// catalog and the live provider listings, mirroring upstream catalog/.
package cataloggen

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"go/format"
	"maps"
	"math"
	"slices"
	"strconv"
	"time"

	"github.com/OrdalieTech/orb/ai"
	"github.com/OrdalieTech/orb/internal/jsonwire"
)

// Sources aggregates the generator inputs. ModelsDev is required. NvidiaNIM,
// OpenRouter, and Vercel are the raw listings from the live provider APIs;
// without a NIM listing NVIDIA has no models, and without the OpenRouter or
// Vercel listing that catalog is omitted entirely (the bundled one keeps
// serving it).
type Sources struct {
	ModelsDev  []byte // models.dev v2 api.json
	NvidiaNIM  []byte // NVIDIA NIM /v1/models listing
	OpenRouter []byte // OpenRouter /api/v1/models listing
	Vercel     []byte // Vercel AI Gateway /v1/models listing
	// GeneratedAt stamps the rendered catalog so stale store overlays lose to
	// a newer bundled catalog (upstream remote-catalog-provider.ts).
	GeneratedAt time.Time
}

type thinking map[ai.ModelThinkingLevel]*string

func (levels thinking) pointer() *map[ai.ModelThinkingLevel]*string {
	if levels == nil {
		return nil
	}
	value := map[ai.ModelThinkingLevel]*string(levels)
	return &value
}

// levels builds a thinking map from level/value pairs; an empty value is null.
func levels(pairs ...string) thinking {
	result := make(thinking, len(pairs)/2)
	for index := 0; index < len(pairs); index += 2 {
		result[ai.ModelThinkingLevel(pairs[index])] = nil
		if pairs[index+1] != "" {
			result[ai.ModelThinkingLevel(pairs[index])] = ptr(pairs[index+1])
		}
	}
	return result
}

// model is a chat model under construction. compat keeps upstream's object
// member order, since the catalog serializes it as written.
type model struct {
	ai.Model
	compat jsonwire.OrderedObject
}

// compat builds an ordered compat object from name/value pairs.
func compat(pairs ...any) jsonwire.OrderedObject {
	object := make(jsonwire.OrderedObject, 0, len(pairs)/2)
	for index := 0; index < len(pairs); index += 2 {
		object = append(object, jsonwire.OrderedMember{Name: pairs[index].(string), Value: pairs[index+1]})
	}
	return object
}

// spread is JavaScript's {...a, ...b}; it never changes its arguments.
func spread(objects ...jsonwire.OrderedObject) jsonwire.OrderedObject {
	result := jsonwire.OrderedObject{}
	for _, object := range objects {
		for _, member := range object {
			result.Set(member.Name, member.Value)
		}
	}
	return result
}

func (m *model) mergeCompat(update jsonwire.OrderedObject) { m.compat = spread(m.compat, update) }

func (m *model) compatValue(name string) any {
	value, _ := m.compat.Value(name)
	return value
}

func (m *model) thinking() thinking {
	if m.ThinkingLevelMap == nil {
		return nil
	}
	return *m.ThinkingLevelMap
}

func (m *model) setThinking(levels thinking) { m.ThinkingLevelMap = maps.Clone(levels).pointer() }

func (m *model) mergeThinking(update thinking) {
	merged := maps.Clone(m.thinking())
	if merged == nil {
		merged = thinking{}
	}
	maps.Copy(merged, update)
	m.ThinkingLevelMap = merged.pointer()
}

func (m *model) hasImageInput() bool { return slices.Contains(m.Input, ai.InputImage) }

// withInputLimits applies provider image limits; limits the model already
// carries take precedence.
func (m *model) withInputLimits(maxRequestBytes, maxPerRequest, maxPerMessage float64) {
	if !m.hasImageInput() {
		return
	}
	limits := ai.ModelInputLimits{Images: &ai.ModelImageInputLimits{}}
	if m.InputLimits != nil {
		limits = *m.InputLimits
		images := ai.ModelImageInputLimits{}
		if limits.Images != nil {
			images = *limits.Images
		}
		limits.Images = &images
	}
	if limits.MaxRequestBytes == nil && maxRequestBytes != 0 {
		limits.MaxRequestBytes = ptr(maxRequestBytes)
	}
	if limits.Images.MaxPerRequest == nil && maxPerRequest != 0 {
		limits.Images.MaxPerRequest = ptr(maxPerRequest)
	}
	if limits.Images.MaxPerMessage == nil && maxPerMessage != 0 {
		limits.Images.MaxPerMessage = ptr(maxPerMessage)
	}
	m.InputLimits = &limits
}

// clone copies the model so that no mutable storage is shared.
func (m *model) clone() *model {
	cloned := &model{Model: *m.Clone(), compat: slices.Clone(m.compat)}
	if m.InputLimits != nil {
		limits := *m.InputLimits
		if limits.Images != nil {
			images := *limits.Images
			limits.Images = &images
		}
		cloned.InputLimits = &limits
	}
	return cloned
}

// addMissing appends additions upstream has not listed (yet).
func addMissing(models []*model, additions ...*model) []*model {
	for _, addition := range additions {
		if !slices.ContainsFunc(models, func(existing *model) bool { return existing.ID == addition.ID }) {
			models = append(models, addition)
		}
	}
	return models
}

// provider is one built-in provider's catalog (upstream ProviderDefinition).
// completionsCompat returns the defaults merged under each openai-completions
// model's own compat.
type provider struct {
	id                string
	models            func(*buildContext) []*model
	completionsCompat func(*model) jsonwire.OrderedObject
	finalize          func(*model, *buildContext)
	finalizeAll       func([]*model)
}

func static(object jsonwire.OrderedObject) func(*model) jsonwire.OrderedObject {
	return func(*model) jsonwire.OrderedObject { return object }
}

type buildContext struct {
	providerID string
	*builder
}

type builder struct {
	modelsDev   map[string][]sourceModel
	nvidiaIDs   map[string]string
	openRouter  []*model
	vercel      []*model
	definitions map[string]provider
	stageOne    map[string][]*model
	building    map[string]bool
	devSources  map[string]sourceModel
}

func modelKey(m *model) string { return string(m.Provider) + ":" + m.ID }

// providerModels returns copies of another provider's models before shared rules run.
func (ctx *buildContext) providerModels(id string) []*model {
	models := ctx.buildModels(id)
	copies := make([]*model, len(models))
	for index, m := range models {
		copies[index] = m.clone()
	}
	return copies
}

func (ctx *buildContext) modelsDevSource(m *model) (sourceModel, bool) {
	source, ok := ctx.devSources[modelKey(m)]
	return source, ok
}

func (b *builder) buildModels(id string) []*model {
	if built, ok := b.stageOne[id]; ok {
		return built
	}
	definition, ok := b.definitions[id]
	if !ok {
		panic("unknown catalog provider " + id)
	}
	if b.building[id] {
		panic("catalog provider " + id + " depends on itself")
	}
	b.building[id] = true
	var models []*model
	if definition.models != nil {
		models = definition.models(&buildContext{providerID: id, builder: b})
	}
	delete(b.building, id)
	for _, m := range models {
		if string(m.Provider) != id {
			panic(id + " produced " + modelKey(m) + " for another provider")
		}
	}
	b.stageOne[id] = models
	return models
}

// devOptions are upstream ModelsDevOptions.
type devOptions struct {
	api              ai.API
	baseURL          string
	compat           jsonwire.OrderedObject
	headers          map[string]string
	protocol         string // only models models.dev lists on this API protocol
	skipDeprecated   bool
	skipReasoning    bool // keep models.dev effort levels away from the shared rules
	defaultContext   float64
	defaultMaxTokens float64
	include          func(sourceModel) bool
	mapModel         func(*model, sourceModel) *model // nil skips the model
}

// modelsDevModels maps one models.dev provider's tool-capable chat models.
func (ctx *buildContext) modelsDevModels(sourceProvider string, options devOptions) []*model {
	var models []*model
	for _, source := range ctx.modelsDev[sourceProvider] {
		if !source.ToolCall ||
			options.protocol != "" && !slices.ContainsFunc(source.APIs, func(api sourceAPI) bool { return api.Protocol == options.protocol }) ||
			options.skipDeprecated && source.Status == "deprecated" ||
			options.include != nil && !options.include(source) {
			continue
		}
		name := source.Name
		if name == "" {
			name = source.ID
		}
		input := ai.InputModalities{ai.InputText}
		if source.ImageInput {
			input = append(input, ai.InputImage)
		}
		m := &model{Model: ai.Model{
			ID: source.ID, Name: name, API: options.api, Provider: ai.ProviderID(ctx.providerID), BaseURL: options.baseURL,
			Reasoning: source.Reasoning, Input: input, Cost: modelsDevCost(source.Cost),
			ContextWindow: firstNonZero(source.ContextWindow, options.defaultContext, 4096),
			MaxTokens:     firstNonZero(source.MaxTokens, options.defaultMaxTokens, 4096),
		}}
		if options.headers != nil {
			headers := maps.Clone(options.headers)
			m.Headers = &headers
		}
		if options.compat != nil {
			m.compat = slices.Clone(options.compat)
		}
		if options.mapModel != nil {
			if m = options.mapModel(m, source); m == nil {
				continue
			}
		}
		if !options.skipReasoning {
			ctx.devSources[modelKey(m)] = source
		}
		models = append(models, m)
	}
	return models
}

// Generate builds every provider's chat catalog from the source listings.
// Radius is deliberately absent.
func Generate(sources Sources) (map[string]map[string]ai.Model, error) {
	modelsDev, err := parseModelsDev(sources.ModelsDev)
	if err != nil {
		return nil, err
	}
	b := &builder{modelsDev: modelsDev, stageOne: map[string][]*model{}, building: map[string]bool{}, devSources: map[string]sourceModel{}}
	if b.nvidiaIDs, err = parseNvidiaIDs(sources.NvidiaNIM); err != nil {
		return nil, err
	}
	definitions := providers()
	b.definitions = make(map[string]provider, len(definitions))
	for _, definition := range definitions {
		b.definitions[definition.id] = definition
	}
	if len(sources.OpenRouter) != 0 {
		if b.openRouter, err = parseOpenRouter(sources.OpenRouter); err != nil {
			return nil, err
		}
	} else {
		delete(b.definitions, "openrouter")
	}
	if len(sources.Vercel) != 0 {
		if b.vercel, err = parseVercel(sources.Vercel); err != nil {
			return nil, err
		}
	} else {
		delete(b.definitions, "vercel-ai-gateway")
	}
	result := make(map[string]map[string]ai.Model)
	for _, definition := range definitions {
		if _, ok := b.definitions[definition.id]; !ok {
			continue
		}
		b.buildModels(definition.id)
	}
	for _, definition := range definitions {
		if _, ok := b.definitions[definition.id]; !ok {
			continue
		}
		ctx := &buildContext{providerID: definition.id, builder: b}
		var chat []*model
		seen := map[string]bool{}
		for _, m := range b.stageOne[definition.id] {
			// The first source listing a model wins.
			if !seen[m.ID] {
				seen[m.ID] = true
				chat = append(chat, m)
			}
		}
		for _, m := range chat {
			if definition.completionsCompat != nil && m.API == ai.APIOpenAICompletions {
				m.compat = spread(definition.completionsCompat(m), m.compat)
			}
			applyDeepSeekCompatRules(m)
			if source, ok := ctx.modelsDevSource(m); ok && source.ReasoningOptions != nil && sendsNativeEffort(m) {
				if effort := effortThinkingLevels(source.ReasoningOptions); effort != nil {
					m.mergeThinking(effort)
				}
			}
			applyOpenAIFamilyRules(m)
			applyClaudeFamilyRules(m)
			applyDeepSeekFamilyRules(m)
		}
		if definition.finalize != nil {
			for _, m := range chat {
				definition.finalize(m, ctx)
			}
		}
		for _, m := range chat {
			applyImageResizeDefaults(m)
		}
		if definition.finalizeAll != nil {
			definition.finalizeAll(chat)
		}
		if len(chat) == 0 {
			continue
		}
		models := make(map[string]ai.Model, len(chat))
		for _, m := range chat {
			m.Compat = nil
			if len(m.compat) != 0 {
				raw, err := json.Marshal(m.compat)
				if err != nil {
					return nil, fmt.Errorf("marshal %s compat: %w", modelKey(m), err)
				}
				m.Compat = raw
			}
			m.Type = "chat"
			models[m.ID] = m.Model
		}
		result[definition.id] = models
	}
	return result, nil
}

// sendsNativeEffort reports whether the model's API sends models.dev effort
// values to the host as-is.
func sendsNativeEffort(m *model) bool {
	switch m.API {
	case ai.APIAnthropicMessages:
		return m.compatValue("forceAdaptiveThinking") == true
	case ai.APIOpenAIResponses, ai.APIAzureOpenAIResponses, ai.APIOpenAICodexResponses:
		return true
	case ai.APIOpenAICompletions:
		format, effort := m.compatValue("thinkingFormat"), m.compatValue("supportsReasoningEffort")
		return (format == nil || format == "openai") && (effort == nil || effort == true)
	}
	return false
}

// applyImageResizeDefaults keeps the generated default no less restrictive
// than the historical cache-safe 2000px / 4.5 MiB image preprocessing.
func applyImageResizeDefaults(m *model) {
	if !m.hasImageInput() {
		return
	}
	m.withInputLimits(0, 0, 0)
	if m.InputLimits.Images.Resize == nil {
		m.InputLimits.Images.Resize = &ai.ModelImageResizeOptions{MaxWidth: ptr(2000.0), MaxHeight: ptr(2000.0), MaxBytes: ptr(4.5 * 1024 * 1024), JPEGQuality: ptr(80.0)}
	}
}

// effortThinkingLevels converts models.dev verified effort values into
// selectable thinking levels (upstream getEffortThinkingLevelMap); values
// without an equivalent (default, null) are omitted.
func effortThinkingLevels(options []reasoningOption) thinking {
	supported := map[string]bool{}
	count := 0
	for _, option := range options {
		if option.Type != "effort" {
			continue
		}
		for _, value := range option.Values {
			count++
			if value != nil {
				supported[*value] = true
			}
		}
	}
	all := []ai.ModelThinkingLevel{ai.ModelThinkingMinimal, ai.ModelThinkingLow, ai.ModelThinkingMedium, ai.ModelThinkingHigh, ai.ModelThinkingXHigh, ai.ModelThinkingMax}
	if count == 0 || !supported["none"] && !slices.ContainsFunc(all, func(level ai.ModelThinkingLevel) bool { return supported[string(level)] }) {
		return nil
	}
	result := thinking{ai.ModelThinkingOff: nil}
	if supported["none"] {
		result[ai.ModelThinkingOff] = ptr("none")
	}
	for _, level := range all {
		result[level] = nil
		if supported[string(level)] {
			result[level] = ptr(string(level))
		}
	}
	return result
}

// Render converts the aggregated source listings into the checked-in Go source.
func Render(sources Sources) ([]byte, error) {
	if sources.GeneratedAt.IsZero() {
		return nil, fmt.Errorf("render model catalog: GeneratedAt is required")
	}
	catalog, err := Generate(sources)
	if err != nil {
		return nil, err
	}
	if err := validateCatalog(catalog); err != nil {
		return nil, err
	}
	normalized, err := json.Marshal(catalog)
	if err != nil {
		return nil, fmt.Errorf("marshal generated model catalog: %w", err)
	}
	var compressed bytes.Buffer
	deflater, err := gzip.NewWriterLevel(&compressed, gzip.BestCompression)
	if err != nil {
		return nil, fmt.Errorf("compress generated model catalog: %w", err)
	}
	if _, err := deflater.Write(normalized); err != nil {
		return nil, fmt.Errorf("compress generated model catalog: %w", err)
	}
	if err := deflater.Close(); err != nil {
		return nil, fmt.Errorf("compress generated model catalog: %w", err)
	}
	var source bytes.Buffer
	source.WriteString("// Code generated by go generate; DO NOT EDIT.\n\npackage models\n\n")
	source.WriteString("// generatedCatalogLastModified is the UnixMilli catalog build time; models-store\n")
	source.WriteString("// overlays that are not newer lose to the bundled catalog.\n")
	fmt.Fprintf(&source, "const generatedCatalogLastModified int64 = %d\n\n", sources.GeneratedAt.UnixMilli())
	source.WriteString("// generatedCatalogGzipJSON is the gzip-compressed provider-keyed catalog\n")
	source.WriteString("// JSON; builtinCatalog gunzips it on first use.\n")
	source.WriteString("var generatedCatalogGzipJSON = []byte(")
	source.WriteString(strconv.Quote(compressed.String()))
	source.WriteString(")\n")
	formatted, err := format.Source(source.Bytes())
	if err != nil {
		return nil, fmt.Errorf("format generated model catalog: %w", err)
	}
	return formatted, nil
}

// validateCatalog mirrors upstream model-data.ts before generated output can
// replace the checked-in catalog.
func validateCatalog(catalog map[string]map[string]ai.Model) error {
	validAPIs := map[ai.API]bool{
		ai.APIOpenAICompletions: true, ai.APIMistralConversations: true, ai.APIOpenAIResponses: true,
		ai.APIAzureOpenAIResponses: true, ai.APIOpenAICodexResponses: true, ai.APIAnthropicMessages: true,
		ai.APIBedrockConverse: true, ai.APIGoogleGenerativeAI: true, ai.APIGoogleVertex: true, ai.APIPiMessages: true,
	}
	for _, provider := range slices.Sorted(maps.Keys(catalog)) {
		models := catalog[provider]
		for _, id := range slices.Sorted(maps.Keys(models)) {
			model := models[id]
			if model.ID != id {
				return fmt.Errorf("validate model catalog: %s/%s declares id %q", provider, id, model.ID)
			}
			if string(model.Provider) != provider {
				return fmt.Errorf("validate model catalog: %s/%s declares provider %q", provider, id, model.Provider)
			}
			if !validAPIs[model.API] {
				return fmt.Errorf("validate model catalog: %s/%s declares unknown api %q", provider, id, model.API)
			}
			if model.Name == "" {
				return fmt.Errorf("validate model catalog: %s/%s has no model name", provider, id)
			}
			if len(model.Input) == 0 {
				return fmt.Errorf("validate model catalog: %s/%s has invalid input modalities", provider, id)
			}
			for _, modality := range model.Input {
				if modality != ai.InputText && modality != ai.InputImage {
					return fmt.Errorf("validate model catalog: %s/%s has invalid input modality %q", provider, id, modality)
				}
			}
			if math.IsNaN(model.ContextWindow) || math.IsInf(model.ContextWindow, 0) || model.ContextWindow <= 0 {
				return fmt.Errorf("validate model catalog: %s/%s has invalid contextWindow", provider, id)
			}
			if math.IsNaN(model.MaxTokens) || math.IsInf(model.MaxTokens, 0) || model.MaxTokens <= 0 {
				return fmt.Errorf("validate model catalog: %s/%s has invalid maxTokens", provider, id)
			}
			for name, cost := range map[string]float64{
				"input": model.Cost.Input, "output": model.Cost.Output,
				"cacheRead": model.Cost.CacheRead, "cacheWrite": model.Cost.CacheWrite,
			} {
				if math.IsNaN(cost) || math.IsInf(cost, 0) {
					return fmt.Errorf("validate model catalog: %s/%s has invalid cost.%s", provider, id, name)
				}
			}
		}
	}
	return nil
}

func ptr[T any](value T) *T { return &value }
