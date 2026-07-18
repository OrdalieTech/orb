package cataloggen

import (
	"bytes"
	"encoding/json"
	"os"
	"testing"
)

func TestRenderMatchesCheckedInCatalog(t *testing.T) {
	data, err := os.ReadFile("../../testdata/api.json")
	if err != nil {
		t.Fatal(err)
	}
	got, err := Render(data)
	if err != nil {
		t.Fatal(err)
	}
	want, err := os.ReadFile("../../generated.go")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("generated.go is stale: rendered %d bytes, checked in %d bytes; run go generate ./ai/models", len(got), len(want))
	}
}

func TestGenerateCommittedSnapshotIsDeterministic(t *testing.T) {
	data, err := os.ReadFile("../../testdata/api.json")
	if err != nil {
		t.Fatal(err)
	}
	first, err := Generate(data)
	if err != nil {
		t.Fatal(err)
	}
	second, err := Generate(data)
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
		t.Fatal("fixed models.dev input generated different catalogs")
	}
	if len(first) < 30 {
		t.Fatalf("generated only %d providers", len(first))
	}
	if _, exists := first["radius"]; exists {
		t.Fatal("Radius must not enter the pi-go catalog")
	}
	model, exists := first["openai"]["gpt-5.4"]
	if !exists {
		t.Fatal("missing openai/gpt-5.4")
	}
	if model.ContextWindow == 0 || model.MaxTokens == 0 || model.Cost.Input == 0 {
		t.Fatalf("incomplete generated model: %#v", model)
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
	catalog, err := Generate(data)
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
