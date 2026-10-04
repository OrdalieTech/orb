package cataloggen

import (
	"bytes"
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/OrdalieTech/orb/ai"
	"github.com/OrdalieTech/orb/conformance/runner"
)

// pinnedGeneratedAt matches the -generated-at value in the ai/models doc.go
// generation directive.
var pinnedGeneratedAt = time.Date(2026, 10, 3, 18, 21, 6, 0, time.UTC)

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
	sources.OpenRouter = readCatalogTestFile(t, "../../testdata/openrouter-current.json")
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
		t.Fatal("Radius must not enter the orb catalog")
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
