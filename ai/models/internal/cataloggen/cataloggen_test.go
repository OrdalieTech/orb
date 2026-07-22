package cataloggen

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/OrdalieTech/pigo/ai"
	"github.com/OrdalieTech/pigo/conformance/runner"
)

// pinnedGeneratedAt matches the -generated-at value in the ai/models doc.go
// generation directive.
var pinnedGeneratedAt = time.Date(2026, 7, 21, 16, 28, 58, 0, time.UTC)

func TestSYNC4GeneratedCatalogTimestampCoversAllSourceCaptures(t *testing.T) {
	doc := readCatalogTestFile(t, "../../doc.go")
	generatedMatch := regexp.MustCompile(`-generated-at ([^[:space:]]+)`).FindSubmatch(doc)
	if len(generatedMatch) != 2 {
		t.Fatal("doc.go has no -generated-at value")
	}
	generatedAt, err := time.Parse(time.RFC3339Nano, string(generatedMatch[1]))
	if err != nil {
		t.Fatalf("parse doc.go -generated-at: %v", err)
	}
	if !pinnedGeneratedAt.Equal(generatedAt) {
		t.Fatalf("test generation time %s differs from doc.go %s", pinnedGeneratedAt, generatedAt)
	}

	readme := readCatalogTestFile(t, "../../testdata/README.md")
	captureMatch := regexp.MustCompile(`all captured by ([0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9:.]+Z):`).FindSubmatch(readme)
	if len(captureMatch) != 2 {
		t.Fatal("catalog snapshot README has no live-source capture timestamp")
	}
	latestCapture, err := time.Parse(time.RFC3339Nano, string(captureMatch[1]))
	if err != nil {
		t.Fatalf("parse live-source capture timestamp: %v", err)
	}
	apiCaptureMatch := regexp.MustCompile("`api\\.json`[^\\n]*fetched on ([0-9]{4}-[0-9]{2}-[0-9]{2}) UTC").FindSubmatch(readme)
	if len(apiCaptureMatch) != 2 {
		t.Fatal("catalog snapshot README has no models.dev capture date")
	}
	apiCapture, err := time.Parse("2006-01-02", string(apiCaptureMatch[1]))
	if err != nil {
		t.Fatalf("parse models.dev capture date: %v", err)
	}
	if !apiCapture.Before(latestCapture) {
		t.Fatalf("models.dev capture date %s is not covered by latest capture %s", apiCapture, latestCapture)
	}
	if generatedAt.Before(latestCapture) {
		t.Fatalf("generated catalog timestamp %s predates source capture %s", generatedAt, latestCapture)
	}

	for _, name := range []string{"api.json", "nvidia-nim.json", "openrouter.json", "vercel.json"} {
		digestPattern := regexp.MustCompile("(?m)`" + regexp.QuoteMeta(name) + "`[^\\n]*SHA-256(?: is)? `([0-9a-f]{64})`")
		digestMatch := digestPattern.FindSubmatch(readme)
		if len(digestMatch) != 2 {
			t.Fatalf("catalog snapshot README has no digest for %s", name)
		}
		content := readCatalogTestFile(t, "../../testdata/"+name)
		digest := sha256.Sum256(content)
		if got, want := fmt.Sprintf("%x", digest), string(digestMatch[1]); got != want {
			t.Fatalf("%s digest = %s, README records %s", name, got, want)
		}
	}

	generated := readCatalogTestFile(t, "../../generated.go")
	millisMatch := regexp.MustCompile(`const generatedCatalogLastModified int64 = ([0-9]+)`).FindSubmatch(generated)
	if len(millisMatch) != 2 {
		t.Fatal("generated.go has no catalog timestamp")
	}
	millis, err := strconv.ParseInt(string(millisMatch[1]), 10, 64)
	if err != nil {
		t.Fatalf("parse generated catalog timestamp: %v", err)
	}
	if millis != generatedAt.UnixMilli() {
		t.Fatalf("generated.go timestamp %d differs from doc.go %d", millis, generatedAt.UnixMilli())
	}
}

func readCatalogTestFile(t *testing.T, path string) []byte {
	t.Helper()
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return content
}

func pinnedSources(t *testing.T) Sources {
	t.Helper()
	sources := Sources{GeneratedAt: pinnedGeneratedAt}
	for _, item := range []struct {
		target *[]byte
		path   string
	}{
		{&sources.ModelsDev, "../../testdata/api.json"},
		{&sources.NvidiaNIM, "../../testdata/nvidia-nim.json"},
		{&sources.OpenRouter, "../../testdata/openrouter.json"},
		{&sources.Vercel, "../../testdata/vercel.json"},
	} {
		data, err := os.ReadFile(item.path)
		if err != nil {
			t.Fatal(err)
		}
		*item.target = data
	}
	return sources
}

func TestRenderMatchesCheckedInCatalog(t *testing.T) {
	sources := pinnedSources(t)
	previous, err := os.ReadFile("../../generated.go")
	if err != nil {
		t.Fatal(err)
	}
	got, err := Render(sources)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, previous) {
		t.Fatalf("generated.go is stale: rendered %d bytes, checked in %d bytes; run go generate ./ai/models", len(got), len(previous))
	}
}

func TestGenerateCommittedSnapshotIsDeterministic(t *testing.T) {
	sources := pinnedSources(t)
	first, err := Generate(sources)
	if err != nil {
		t.Fatal(err)
	}
	second, err := Generate(sources)
	if err != nil {
		t.Fatal(err)
	}
	firstJSON, err := json.Marshal(first)
	if err != nil {
		t.Fatal(err)
	}
	secondJSON, err := json.Marshal(second)
	if err != nil {
		t.Fatal(err)
	}
	if string(firstJSON) != string(secondJSON) {
		t.Fatal("fixed source inputs generated different catalogs")
	}
	if len(first) < 30 {
		t.Fatalf("generated only %d providers", len(first))
	}
	if _, exists := first["radius"]; exists {
		t.Fatal("Radius must not enter the pigo catalog")
	}
	model, exists := first["openai"]["gpt-5.4"]
	if !exists {
		t.Fatal("missing openai/gpt-5.4")
	}
	if model.ContextWindow == 0 || model.MaxTokens == 0 || model.Cost.Input == 0 {
		t.Fatalf("incomplete generated model: %#v", model)
	}
}

func TestCompatModelsMatchPinnedF2Models(t *testing.T) {
	catalog, err := Generate(pinnedSources(t))
	if err != nil {
		t.Fatal(err)
	}

	var fixture struct {
		Cases []struct {
			Name  string   `json:"name"`
			Model ai.Model `json:"model"`
		} `json:"cases"`
	}
	runner.LoadJSON(t, "F2", "compat-models.json", &fixture)
	for _, item := range fixture.Cases {
		key := string(item.Model.Provider) + "/" + item.Model.ID
		got, ok := catalog[string(item.Model.Provider)][item.Model.ID]
		if !ok {
			t.Fatalf("generated catalog is missing %s", key)
		}
		gotJSON, _ := json.Marshal(got)
		wantJSON, _ := json.Marshal(item.Model)
		if string(gotJSON) != string(wantJSON) {
			t.Fatalf("%s (%s) metadata mismatch\n got: %s\nwant: %s", key, item.Name, gotJSON, wantJSON)
		}
	}
}

func TestGenerateFiltersUnsupportedSourceModels(t *testing.T) {
	data := []byte(`{
		"anthropic":{"models":{
			"kept":{"name":"Kept","tool_call":true,"reasoning":true,"modalities":{"input":["text","image"]},"limit":{"context":100,"output":20},"cost":{"input":1,"output":2,"cache_read":0.1,"cache_write":1}},
			"no-tools":{"tool_call":false},
			"old":{"tool_call":true,"status":"deprecated"}
		}}
	}`)
	catalog, err := Generate(Sources{ModelsDev: data})
	if err != nil {
		t.Fatal(err)
	}
	models := catalog["anthropic"]
	if len(models) != 2 {
		t.Fatalf("got %d anthropic models, want 2", len(models))
	}
	model := models["kept"]
	if !model.Reasoning || len(model.Input) != 2 || model.Cost.CacheRead != 0.1 {
		t.Fatalf("bad normalized model: %#v", model)
	}
	if _, exists := models["old"]; !exists {
		t.Fatal("pinned generator keeps deprecated Anthropic entries")
	}
}

func TestGenerateFreshUpstreamCatalogChanges(t *testing.T) {
	catalog, err := Generate(pinnedSources(t))
	if err != nil {
		t.Fatal(err)
	}
	for _, provider := range []string{"qwen-token-plan", "qwen-token-plan-cn"} {
		if len(catalog[provider]) != 15 {
			t.Fatalf("%s models = %d, want 15", provider, len(catalog[provider]))
		}
	}
	if _, ok := catalog["kimi-coding"]["k2p7"]; ok {
		t.Fatal("legacy Kimi k2p7 alias remains in catalog")
	}
	if _, ok := catalog["kimi-coding"]["kimi-for-coding"]; !ok {
		t.Fatal("canonical Kimi coding model is missing")
	}
	for _, provider := range []string{"opencode", "opencode-go"} {
		if got := catalog[provider]["grok-4.5"].API; got != ai.APIOpenAIResponses {
			t.Fatalf("%s grok API = %q", provider, got)
		}
	}
	for provider, ids := range map[string][]string{
		"nvidia":            {"qwen/qwen3.5-122b-a10b"},
		"openrouter":        {"meta-llama/llama-3.3-70b-instruct:free", "qwen/qwen3-coder:free", "qwen/qwen3-next-80b-a3b-instruct:free"},
		"together":          {"Qwen/Qwen3-235B-A22B-Instruct-2507-tput", "Qwen/Qwen3.5-397B-A17B", "essentialai/Rnj-1-Instruct", "zai-org/GLM-5", "zai-org/GLM-5.1"},
		"vercel-ai-gateway": {"meta/llama-3.2-11b", "meta/llama-3.2-90b"},
	} {
		for _, id := range ids {
			if _, ok := catalog[provider][id]; ok {
				t.Fatalf("removed model remains in catalog: %s/%s", provider, id)
			}
		}
	}
	if _, ok := catalog["together"]["thinkingmachines/Inkling"]; !ok {
		t.Fatal("Together Inkling model is missing")
	}
	for _, model := range []string{"gpt-5.6-luna", "gpt-5.6-sol", "gpt-5.6-terra"} {
		if got := catalog["openai-codex"][model].ContextWindow; got != 272000 {
			t.Fatalf("OpenAI Codex %s context = %v", model, got)
		}
	}
}

func TestV0811CatalogDeltasMatchPublishedPackage(t *testing.T) {
	catalog, err := Generate(pinnedSources(t))
	if err != nil {
		t.Fatal(err)
	}
	for provider, want := range map[string]struct {
		count int
		hash  string
	}{
		"google":            {18, "26b54727f38b22f753c6f85fcfd790f9be0c60ca19b94444326fc400feaddd05"},
		"opencode":          {55, "fe54453668b9fb5f805067f8a1cf5f9ff22a603daab975719606e851de34edbd"},
		"openrouter":        {271, "76a3833444dbe6dab7a6edd4fd29915efe3047634b0b8909eb73111086ca0cb4"},
		"vercel-ai-gateway": {190, "312246de110840fbe5eea7ed506dd387aaebd77f26e3c7e01e868220808658a2"},
	} {
		if got := len(catalog[provider]); got != want.count {
			t.Errorf("%s model count = %d, published v0.81.1 has %d", provider, got, want.count)
		}
		if got := modelIDSetHash(catalog[provider]); got != want.hash {
			t.Errorf("%s ID set hash = %s, published v0.81.1 has %s", provider, got, want.hash)
		}
	}
	var fixture struct {
		SchemaVersion int `json:"schemaVersion"`
		Source        struct {
			Package       string `json:"package"`
			Version       string `json:"version"`
			TarballSHA256 string `json:"tarballSHA256"`
		} `json:"source"`
		Models []ai.Model `json:"models"`
	}
	if err := json.Unmarshal(readCatalogTestFile(t, "../../testdata/v0.81.1-model-deltas.json"), &fixture); err != nil {
		t.Fatal(err)
	}
	if fixture.SchemaVersion != 1 || fixture.Source.Package != "@earendil-works/pi-ai" || fixture.Source.Version != "0.81.1" ||
		fixture.Source.TarballSHA256 != "c79dcc0f90d4dfbd1974da33dfa3fe396663195a68339b1f55c114dbf7240f2f" {
		t.Fatalf("unexpected v0.81.1 fixture provenance: %#v", fixture.Source)
	}
	if len(fixture.Models) != 10 {
		t.Fatalf("v0.81.1 delta fixture has %d models, want 10", len(fixture.Models))
	}
	for _, want := range fixture.Models {
		got, ok := catalog[string(want.Provider)][want.ID]
		if !ok {
			t.Errorf("generated catalog is missing v0.81.1 model %s/%s", want.Provider, want.ID)
			continue
		}
		gotJSON, err := json.Marshal(got)
		if err != nil {
			t.Fatal(err)
		}
		wantJSON, err := json.Marshal(want)
		if err != nil {
			t.Fatal(err)
		}
		var gotValue, wantValue any
		if err := json.Unmarshal(gotJSON, &gotValue); err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(wantJSON, &wantValue); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(gotValue, wantValue) {
			t.Errorf("%s/%s differs from published v0.81.1 package\n got: %s\nwant: %s", want.Provider, want.ID, gotJSON, wantJSON)
		}
	}
}

// CAT-M1: models.dev nvidia must be intersected with the live NIM listing,
// with underscore-to-dot ID normalization before the denylist check.
func TestCATM1NvidiaCatalogIntersectsLiveNIMListing(t *testing.T) {
	wantIDs := []string{
		"meta/llama-3.1-70b-instruct", "meta/llama-3.1-8b-instruct", "meta/llama-3.2-11b-vision-instruct",
		"meta/llama-3.2-90b-vision-instruct", "meta/llama-3.3-70b-instruct", "minimaxai/minimax-m3",
		"mistralai/mistral-large-3-675b-instruct-2512", "mistralai/mistral-small-4-119b-2603",
		"moonshotai/kimi-k2.6", "nvidia/nemotron-3-nano-30b-a3b", "nvidia/nemotron-3-nano-omni-30b-a3b-reasoning",
		"nvidia/nemotron-3-super-120b-a12b", "nvidia/nemotron-3-ultra-550b-a55b", "nvidia/nvidia-nemotron-nano-9b-v2",
		"openai/gpt-oss-120b", "openai/gpt-oss-20b", "stepfun-ai/step-3.5-flash", "stepfun-ai/step-3.7-flash",
		"z-ai/glm-5.2",
	}
	catalog, err := Generate(pinnedSources(t))
	if err != nil {
		t.Fatal(err)
	}
	nvidia := catalog["nvidia"]
	if len(nvidia) != 19 {
		t.Fatalf("nvidia models = %d, want 19", len(nvidia))
	}
	for _, id := range wantIDs {
		if _, ok := nvidia[id]; !ok {
			t.Fatalf("missing NIM-served model nvidia/%s", id)
		}
	}
	for _, id := range []string{
		// models.dev-only entries that NIM does not serve.
		"mistralai/mixtral-8x22b-instruct", "moonshotai/kimi-k2-instruct-0905",
		"nvidia/nemotron-voicechat", "qwen/qwen2.5-coder-32b-instruct",
		// denylisted after underscore normalization against the live listing id.
		"abacusai/dracarys-llama-3_1-70b-instruct", "abacusai/dracarys-llama-3.1-70b-instruct",
		"upstage/solar-10_7b-instruct", "upstage/solar-10.7b-instruct",
	} {
		if _, ok := nvidia[id]; ok {
			t.Fatalf("leaked invalid nvidia model %s", id)
		}
	}
	model := nvidia["z-ai/glm-5.2"]
	if model.API != ai.APIOpenAICompletions || model.BaseURL != nvidiaBaseURL || (*model.Headers)["NVCF-POLL-SECONDS"] != "3600" {
		t.Fatalf("nvidia model metadata = %#v", model)
	}
}

// CAT-M1: upstream's non-strict fetch fallback is an empty NIM map, so a
// missing listing must omit NVIDIA rather than trusting models.dev alone.
func TestCATM1NvidiaOmittedWithoutNIMListing(t *testing.T) {
	sources := pinnedSources(t)
	sources.NvidiaNIM = nil
	catalog, err := Generate(sources)
	if err != nil {
		t.Fatal(err)
	}
	if len(catalog["nvidia"]) != 0 {
		t.Fatalf("nvidia models = %d without a NIM listing, want zero", len(catalog["nvidia"]))
	}
}

// CAT-M2: qwen3.8-max-preview is hardcoded for both Qwen Token Plan providers
// (generate-models.ts:2281-2303) until models.dev includes it.
func TestCATM2QwenTokenPlanMaxPreviewInjection(t *testing.T) {
	catalog, err := Generate(pinnedSources(t))
	if err != nil {
		t.Fatal(err)
	}
	for provider, baseURL := range map[string]string{
		"qwen-token-plan":    "https://token-plan.ap-southeast-1.maas.aliyuncs.com/compatible-mode/v1",
		"qwen-token-plan-cn": "https://token-plan.cn-beijing.maas.aliyuncs.com/compatible-mode/v1",
	} {
		model, ok := catalog[provider]["qwen3.8-max-preview"]
		if !ok {
			t.Fatalf("missing %s/qwen3.8-max-preview", provider)
		}
		if !model.Reasoning || model.ContextWindow != 1000000 || model.MaxTokens != 65536 || model.BaseURL != baseURL {
			t.Fatalf("%s/qwen3.8-max-preview = %#v", provider, model)
		}
		if len(model.Input) != 2 || model.Input[0] != ai.InputText || model.Input[1] != ai.InputImage {
			t.Fatalf("%s/qwen3.8-max-preview input = %#v", provider, model.Input)
		}
		var compat ai.OpenAICompletionsCompat
		if err := json.Unmarshal(model.Compat, &compat); err != nil {
			t.Fatal(err)
		}
		if compat.ThinkingFormat == nil || *compat.ThinkingFormat != ai.ThinkingFormatQwen ||
			compat.SupportsDeveloperRole == nil || *compat.SupportsDeveloperRole ||
			compat.SupportsStore == nil || *compat.SupportsStore {
			t.Fatalf("%s/qwen3.8-max-preview compat = %s", provider, model.Compat)
		}
	}
}

// CAT-M2: the injection is guarded; a models.dev-provided entry wins.
func TestCATM2QwenTokenPlanMaxPreviewInjectionIsGuarded(t *testing.T) {
	data := []byte(`{
		"alibaba-token-plan":{"models":{
			"qwen3.8-max-preview":{"name":"From models.dev","tool_call":true,"reasoning":true,"modalities":{"input":["text"]},"limit":{"context":42,"output":7},"cost":{"input":1,"output":2}}
		}}
	}`)
	catalog, err := Generate(Sources{ModelsDev: data})
	if err != nil {
		t.Fatal(err)
	}
	model := catalog["qwen-token-plan"]["qwen3.8-max-preview"]
	if model.Name != "From models.dev" || model.ContextWindow != 42 {
		t.Fatalf("models.dev entry was overwritten: %#v", model)
	}
	if _, ok := catalog["qwen-token-plan-cn"]["qwen3.8-max-preview"]; !ok {
		t.Fatal("missing injected qwen-token-plan-cn/qwen3.8-max-preview")
	}
}

// CAT-M3: the OpenRouter catalog comes from the live /api/v1/models listing
// filtered on tools support, not from models.dev.
func TestCATM3OpenRouterCatalogFromLiveListing(t *testing.T) {
	catalog, err := Generate(pinnedSources(t))
	if err != nil {
		t.Fatal(err)
	}
	openrouter := catalog["openrouter"]
	if len(openrouter) != 271 {
		t.Fatalf("openrouter models = %d, want 271", len(openrouter))
	}
	// Byte-identical to the 271 IDs in the published v0.81.1 manifest.
	if got, want := modelIDSetHash(openrouter), "76a3833444dbe6dab7a6edd4fd29915efe3047634b0b8909eb73111086ca0cb4"; got != want {
		t.Fatalf("openrouter ID set hash = %s, want %s", got, want)
	}
	model, ok := openrouter["anthropic/claude-sonnet-4.5"]
	if !ok {
		t.Fatal("missing openrouter/anthropic/claude-sonnet-4.5")
	}
	if model.API != ai.APIOpenAICompletions || model.BaseURL != "https://openrouter.ai/api/v1" || !model.Reasoning {
		t.Fatalf("openrouter model shape = %#v", model)
	}
	if model.Cost.Input != 3 || model.Cost.Output != 15 || model.Cost.CacheRead != 0.3 || model.Cost.CacheWrite != 3.75 {
		t.Fatalf("openrouter pricing not converted to $/1M tokens: %#v", model.Cost)
	}
	if model.ContextWindow != 1000000 || model.MaxTokens != 64000 {
		t.Fatalf("openrouter limits = %v/%v", model.ContextWindow, model.MaxTokens)
	}
	if len(model.Input) != 2 {
		t.Fatalf("openrouter modality mapping = %#v", model.Input)
	}
	// SYNC-3: dropped upstream in 890b3547 and absent from the live listing.
	if _, ok := openrouter["tencent/hy3:free"]; ok {
		t.Fatal("tencent/hy3:free must not re-enter the catalog")
	}
	for _, alias := range []string{"auto", "openrouter/fusion"} {
		if _, ok := openrouter[alias]; !ok {
			t.Fatalf("missing openrouter alias %q", alias)
		}
	}
}

// CAT-M3: the Vercel AI Gateway catalog comes from the live /v1/models listing
// filtered on the tool-use tag; every model uses anthropic-messages.
func TestCATM3VercelCatalogFromLiveListing(t *testing.T) {
	catalog, err := Generate(pinnedSources(t))
	if err != nil {
		t.Fatal(err)
	}
	gateway := catalog["vercel-ai-gateway"]
	if len(gateway) != 190 {
		t.Fatalf("vercel-ai-gateway models = %d, want 190", len(gateway))
	}
	// Byte-identical to the 190 IDs in the published v0.81.1 manifest.
	if got, want := modelIDSetHash(gateway), "312246de110840fbe5eea7ed506dd387aaebd77f26e3c7e01e868220808658a2"; got != want {
		t.Fatalf("vercel-ai-gateway ID set hash = %s, want %s", got, want)
	}
	for id, model := range gateway {
		if model.API != ai.APIAnthropicMessages || model.BaseURL != "https://ai-gateway.vercel.sh" {
			t.Fatalf("vercel-ai-gateway/%s API routing = %#v", id, model)
		}
	}
	model, ok := gateway["alibaba/qwen-3-14b"]
	if !ok {
		t.Fatal("missing vercel-ai-gateway/alibaba/qwen-3-14b")
	}
	if model.Cost.Input != 0.12 || model.Cost.Output != 0.24 || !model.Reasoning {
		t.Fatalf("vercel-ai-gateway pricing/tags = %#v", model)
	}
	if model.ContextWindow != 40960 || model.MaxTokens != 16384 {
		t.Fatalf("vercel-ai-gateway limits = %v/%v", model.ContextWindow, model.MaxTokens)
	}
}

func modelIDSetHash(models map[string]ai.Model) string {
	ids := sortedKeys(models)
	digest := sha256.Sum256([]byte(strings.Join(ids, "\n") + "\n"))
	return fmt.Sprintf("%x", digest)
}

// CAT-M3: without live listings both catalogs are omitted so the bundled
// catalog keeps serving them; models.dev must not fill them in.
func TestCATM3LiveCatalogsOmittedWithoutListings(t *testing.T) {
	sources := pinnedSources(t)
	sources.OpenRouter, sources.Vercel = nil, nil
	catalog, err := Generate(sources)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := catalog["vercel-ai-gateway"]; ok {
		t.Fatal("vercel-ai-gateway must be omitted without the live listing")
	}
	for id := range catalog["openrouter"] {
		if id != "auto" && id != "openrouter/fusion" {
			t.Fatalf("openrouter %q generated without the live listing", id)
		}
	}
}

// SYNC-3: hy3-free was dropped upstream (890b3547); the pinned models.dev
// snapshot still lists it, so the generator excludes it explicitly.
func TestSYNC3OpenCodeHy3FreeExcluded(t *testing.T) {
	catalog, err := Generate(pinnedSources(t))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := catalog["opencode"]["hy3-free"]; ok {
		t.Fatal("opencode/hy3-free must not re-enter the catalog")
	}
	if len(catalog["opencode"]) != 55 {
		t.Fatalf("opencode models = %d, want 55", len(catalog["opencode"]))
	}
}

// SYNC-5: Render validates the catalog before it can replace generated.go.
func TestSYNC5RenderValidatesBeforeWrite(t *testing.T) {
	minimal := []byte(`{"anthropic":{"models":{"kept":{"tool_call":true,"modalities":{"input":["text"]},"limit":{"context":10,"output":5},"cost":{"input":1,"output":2}}}}}`)
	_, err := Render(Sources{ModelsDev: minimal, GeneratedAt: pinnedGeneratedAt})
	if err != nil {
		t.Fatal(err)
	}

	if _, err := Render(Sources{ModelsDev: minimal}); err == nil || !strings.Contains(err.Error(), "GeneratedAt") {
		t.Fatalf("Render accepted a zero GeneratedAt: %v", err)
	}

}

// SYNC-5: structural validation mirrors model-data.ts, including runtime model
// metadata needed by every generated entry.
func TestSYNC5ValidateCatalogStructure(t *testing.T) {
	validModel := ai.Model{
		ID: "m", Name: "Model", Provider: "anthropic", API: ai.APIAnthropicMessages,
		BaseURL: "", Input: ai.InputModalities{ai.InputText}, ContextWindow: 10, MaxTokens: 5,
		Cost: ai.ModelCost{ModelCostRates: ai.ModelCostRates{}},
	}
	valid := map[string]map[string]ai.Model{
		"anthropic": {"m": validModel},
	}
	if err := validateCatalog(valid); err != nil {
		t.Fatalf("validateCatalog rejected a valid catalog: %v", err)
	}
	if err := validateCatalog(map[string]map[string]ai.Model{"anthropic": {}}); err != nil {
		t.Fatalf("validateCatalog rejected a structurally consistent empty provider: %v", err)
	}
	invalidModality := validModel
	invalidModality.Input = ai.InputModalities{ai.InputModality("audio")}
	emptyName := validModel
	emptyName.Name = ""
	emptyInput := validModel
	emptyInput.Input = nil
	invalidContext := validModel
	invalidContext.ContextWindow = math.NaN()
	invalidMaxTokens := validModel
	invalidMaxTokens.MaxTokens = 0
	invalidCost := validModel
	invalidCost.Cost.Input = math.Inf(1)
	for name, catalog := range map[string]map[string]map[string]ai.Model{
		"id mismatch":    {"anthropic": {"m": withModelID(validModel, "other")}},
		"provider drift": {"anthropic": {"m": withModelProvider(validModel, "openai")}},
		"unknown api":    {"anthropic": {"m": withModelAPI(validModel, ai.API("wat"))}},
		"empty name":     {"anthropic": {"m": emptyName}},
		"empty input":    {"anthropic": {"m": emptyInput}},
		"bad modality":   {"anthropic": {"m": invalidModality}},
		"bad context":    {"anthropic": {"m": invalidContext}},
		"bad max tokens": {"anthropic": {"m": invalidMaxTokens}},
		"bad cost":       {"anthropic": {"m": invalidCost}},
	} {
		if err := validateCatalog(catalog); err == nil {
			t.Fatalf("validateCatalog accepted %s", name)
		}
	}
}

func withModelID(model ai.Model, id string) ai.Model {
	model.ID = id
	return model
}

func withModelProvider(model ai.Model, provider ai.ProviderID) ai.Model {
	model.Provider = provider
	return model
}

func withModelAPI(model ai.Model, api ai.API) ai.Model {
	model.API = api
	return model
}
