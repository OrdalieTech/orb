package runner_test

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"strconv"
	"testing"

	"github.com/OrdalieTech/pigo/agent"
	"github.com/OrdalieTech/pigo/ai"
	"github.com/OrdalieTech/pigo/codingagent/tools"
	"github.com/OrdalieTech/pigo/conformance/runner"
)

type wp440ReadFixture struct {
	SchemaVersion int             `json:"schemaVersion"`
	InputBase64   string          `json:"inputBase64"`
	InputSHA256   string          `json:"inputSHA256"`
	Cases         []wp440ReadCase `json:"cases"`
}

type wp440ReadCase struct {
	Name       string   `json:"name"`
	ModelInput []string `json:"modelInput"`
	Expected   struct {
		Content []wp440ReadBlock `json:"content"`
	} `json:"expected"`
}

type wp440ReadBlock struct {
	Type     string `json:"type"`
	Text     string `json:"text"`
	Data     string `json:"data"`
	MimeType string `json:"mimeType"`
}

type wp440ReadOperations struct{ data []byte }

func (operations wp440ReadOperations) ReadFile(context.Context, string) ([]byte, error) {
	return append([]byte(nil), operations.data...), nil
}

func (wp440ReadOperations) Access(context.Context, string) error { return nil }

func (wp440ReadOperations) DetectImageMimeType(context.Context, string) (string, error) {
	return "image/png", nil
}

func TestWP440ReadImageResultMatchesUpstream(t *testing.T) {
	manifest := runner.LoadManifest(t, "WP440Read")
	if manifest.Family != "WP440Read" || manifest.Generator != "conformance/extract/wp440-read.ts" {
		t.Fatalf("unexpected WP440Read manifest: %+v", manifest)
	}
	var fixture wp440ReadFixture
	runner.LoadJSON(t, "WP440Read", "read.json", &fixture)
	if fixture.SchemaVersion != 1 || len(fixture.Cases) != 2 {
		t.Fatalf("WP440Read fixture header = version %d, cases %d", fixture.SchemaVersion, len(fixture.Cases))
	}
	input, err := base64.StdEncoding.DecodeString(fixture.InputBase64)
	if err != nil {
		t.Fatal(err)
	}
	hash := sha256.Sum256(input)
	if got := hex.EncodeToString(hash[:]); got != fixture.InputSHA256 {
		t.Fatalf("input SHA-256 = %s, want %s", got, fixture.InputSHA256)
	}
	autoResizeImages := false
	tool := tools.NewReadTool("/fixture", &tools.ReadToolOptions{
		Operations:       wp440ReadOperations{data: input},
		AutoResizeImages: &autoResizeImages,
	})
	for _, fixtureCase := range fixture.Cases {
		t.Run(fixtureCase.Name, func(t *testing.T) {
			modalities := make(ai.InputModalities, len(fixtureCase.ModelInput))
			for index, modality := range fixtureCase.ModelInput {
				modalities[index] = ai.InputModality(modality)
			}
			ctx := agent.WithToolExecutionModel(context.Background(), &ai.Model{Input: modalities})
			got, err := tool.Execute(ctx, "fixture-call", map[string]any{"path": "fixture.png"}, nil)
			if err != nil {
				t.Fatal(err)
			}
			blocks := make([]wp440ReadBlock, 0, len(got.Content))
			for _, block := range got.Content {
				switch value := block.(type) {
				case *ai.TextContent:
					blocks = append(blocks, wp440ReadBlock{Type: "text", Text: value.Text})
				case *ai.ImageContent:
					blocks = append(blocks, wp440ReadBlock{Type: "image", Data: value.Data, MimeType: value.MimeType})
				default:
					t.Fatalf("unexpected content block %T", block)
				}
			}
			if diff := wp440ReadBlocksDiff(fixtureCase.Expected.Content, blocks); diff != "" {
				t.Fatal(diff)
			}
		})
	}
}

func wp440ReadBlocksDiff(want, got []wp440ReadBlock) string {
	if len(got) != len(want) {
		return "content block count differs"
	}
	for index := range want {
		if got[index] != want[index] {
			return "content block differs at index " + strconv.Itoa(index)
		}
	}
	return ""
}
