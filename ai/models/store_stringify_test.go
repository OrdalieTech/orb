package models

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/OrdalieTech/orb/ai"
)

// nodeStoreStringifyGolden is JSON.stringify(store, null, 2) for the store
// written below, generated with:
//
//	node -e 'const store={"custom-provider":{models:[{id:"m<1>",name:"Ünicode ✓ & <custom>",
//	  api:"openai-completions",provider:"custom-provider",baseUrl:"https://x.example/v1?a=1&b=2",
//	  reasoning:false,input:["text"],cost:{input:0,output:0,cacheRead:0,cacheWrite:0},
//	  contextWindow:8192,maxTokens:1024}],checkedAt:1700000000000,lastModified:0,
//	  etag:"W/\"<etag&>\""}};process.stdout.write(JSON.stringify(store,null,2))'
const nodeStoreStringifyGolden = `{
  "custom-provider": {
    "models": [
      {
        "id": "m<1>",
        "name": "Ünicode ✓ & <custom>",
        "api": "openai-completions",
        "provider": "custom-provider",
        "baseUrl": "https://x.example/v1?a=1&b=2",
        "reasoning": false,
        "input": [
          "text"
        ],
        "cost": {
          "input": 0,
          "output": 0,
          "cacheRead": 0,
          "cacheWrite": 0
        },
        "contextWindow": 8192,
        "maxTokens": 1024
      }
    ],
    "checkedAt": 1700000000000,
    "lastModified": 0,
    "etag": "W/\"<etag&>\""
  }
}`

func TestWriteStoreMatchesNodeStringifyBytes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "models-store.json")
	model := ai.Model{
		ID:            "m<1>",
		Name:          "Ünicode ✓ & <custom>",
		API:           "openai-completions",
		Provider:      "custom-provider",
		BaseURL:       "https://x.example/v1?a=1&b=2",
		Input:         ai.InputModalities{ai.InputText},
		ContextWindow: 8192,
		MaxTokens:     1024,
	}
	catalog := &Catalog{providers: map[string]map[string]ai.Model{"custom-provider": {model.ID: model}}}
	if err := writeStoreResponse(path, catalog, 1700000000000, storeTimestamp(0), `W/"<etag&>"`); err != nil {
		t.Fatal(err)
	}
	written, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(written) != nodeStoreStringifyGolden {
		t.Fatalf("models-store.json diverges from JSON.stringify(store, null, 2)\n--- got ---\n%s\n--- want ---\n%s", written, nodeStoreStringifyGolden)
	}
}
