package runner_test

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/OrdalieTech/pi-go/codingagent/tools"
	"github.com/OrdalieTech/pi-go/conformance/runner"
)

type f4Fixture struct {
	SchemaVersion int      `json:"schemaVersion"`
	Cases         []f4Case `json:"cases"`
}

type f4Case struct {
	Name          string          `json:"name"`
	Operation     string          `json:"operation"`
	UpstreamCase  string          `json:"upstreamCase"`
	Content       *string         `json:"content"`
	OldText       *string         `json:"oldText"`
	NewText       *string         `json:"newText"`
	Edits         []tools.Edit    `json:"edits"`
	Path          *string         `json:"path"`
	Ending        *string         `json:"ending"`
	ContextLines  *int            `json:"contextLines"`
	Expected      json.RawMessage `json:"expected"`
	ExpectedError string          `json:"expectedError"`
}

type f4StripBOMResult struct {
	BOM  string `json:"bom"`
	Text string `json:"text"`
}

type f4PipelineResult struct {
	FinalContent     string `json:"finalContent"`
	BaseContent      string `json:"baseContent"`
	NewContent       string `json:"newContent"`
	Diff             string `json:"diff"`
	FirstChangedLine *int   `json:"firstChangedLine,omitempty"`
	Patch            string `json:"patch"`
}

func TestF4EditDiffMatchesUpstream(t *testing.T) {
	manifest := runner.LoadManifest(t, "F4")
	if manifest.Family != "F4" || manifest.Generator != "conformance/extract/f4-edit.ts" {
		t.Fatalf("unexpected F4 manifest: %+v", manifest)
	}

	var fixture f4Fixture
	runner.LoadJSON(t, "F4", "cases.json", &fixture)
	if fixture.SchemaVersion != 1 || len(fixture.Cases) != 65 {
		t.Fatalf("F4 fixture header = version %d, cases %d", fixture.SchemaVersion, len(fixture.Cases))
	}

	for _, fixtureCase := range fixture.Cases {
		fixtureCase := fixtureCase
		t.Run(fixtureCase.Name, func(t *testing.T) {
			actual, err := runF4Case(fixtureCase)
			if fixtureCase.ExpectedError != "" {
				if err == nil {
					t.Fatalf("%s: expected error %q, got result %#v", fixtureCase.UpstreamCase, fixtureCase.ExpectedError, actual)
				}
				if err.Error() != fixtureCase.ExpectedError {
					t.Fatalf("%s: error\nwant: %q\n got: %q", fixtureCase.UpstreamCase, fixtureCase.ExpectedError, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("%s: %v", fixtureCase.UpstreamCase, err)
			}

			gotJSON, err := json.Marshal(actual)
			if err != nil {
				t.Fatalf("%s: marshal Go result: %v", fixtureCase.UpstreamCase, err)
			}
			wantCanonical, err := runner.CanonicalJSON(fixtureCase.Expected)
			if err != nil {
				t.Fatalf("%s: canonicalize fixture: %v", fixtureCase.UpstreamCase, err)
			}
			gotCanonical, err := runner.CanonicalJSON(gotJSON)
			if err != nil {
				t.Fatalf("%s: canonicalize Go result: %v", fixtureCase.UpstreamCase, err)
			}
			if diff := runner.ByteDiff(wantCanonical, gotCanonical); diff != "" {
				t.Fatalf("%s:\n%s", fixtureCase.UpstreamCase, diff)
			}
		})
	}
}

func runF4Case(fixtureCase f4Case) (any, error) {
	switch fixtureCase.Operation {
	case "detect-line-ending":
		content, err := f4RequiredString(fixtureCase.Content, "content", fixtureCase.Name)
		if err != nil {
			return nil, err
		}
		return tools.DetectLineEnding(content), nil
	case "normalize-lf":
		content, err := f4RequiredString(fixtureCase.Content, "content", fixtureCase.Name)
		if err != nil {
			return nil, err
		}
		return tools.NormalizeToLF(content), nil
	case "restore-line-endings":
		content, err := f4RequiredString(fixtureCase.Content, "content", fixtureCase.Name)
		if err != nil {
			return nil, err
		}
		ending, err := f4RequiredString(fixtureCase.Ending, "ending", fixtureCase.Name)
		if err != nil {
			return nil, err
		}
		return tools.RestoreLineEndings(content, ending), nil
	case "strip-bom":
		content, err := f4RequiredString(fixtureCase.Content, "content", fixtureCase.Name)
		if err != nil {
			return nil, err
		}
		bom, text := tools.StripBOM(content)
		return f4StripBOMResult{BOM: bom, Text: text}, nil
	case "normalize-fuzzy":
		content, err := f4RequiredString(fixtureCase.Content, "content", fixtureCase.Name)
		if err != nil {
			return nil, err
		}
		return tools.NormalizeForFuzzyMatch(content), nil
	case "fuzzy-find":
		content, err := f4RequiredString(fixtureCase.Content, "content", fixtureCase.Name)
		if err != nil {
			return nil, err
		}
		oldText, err := f4RequiredString(fixtureCase.OldText, "oldText", fixtureCase.Name)
		if err != nil {
			return nil, err
		}
		return tools.FuzzyFindText(content, oldText), nil
	case "apply":
		content, path, err := f4ContentAndPath(fixtureCase)
		if err != nil {
			return nil, err
		}
		return tools.ApplyEditsToNormalizedContent(content, fixtureCase.Edits, path)
	case "edit-pipeline":
		return runF4Pipeline(fixtureCase)
	case "diff":
		oldText, newText, err := f4OldAndNew(fixtureCase)
		if err != nil {
			return nil, err
		}
		return tools.GenerateDiffString(oldText, newText, f4ContextLines(fixtureCase)), nil
	case "patch":
		oldText, newText, err := f4OldAndNew(fixtureCase)
		if err != nil {
			return nil, err
		}
		path, err := f4RequiredString(fixtureCase.Path, "path", fixtureCase.Name)
		if err != nil {
			return nil, err
		}
		return tools.GenerateUnifiedPatch(path, oldText, newText, f4ContextLines(fixtureCase))
	default:
		return nil, fmt.Errorf("F4 case %q has unknown operation %q", fixtureCase.Name, fixtureCase.Operation)
	}
}

func runF4Pipeline(fixtureCase f4Case) (f4PipelineResult, error) {
	rawContent, path, err := f4ContentAndPath(fixtureCase)
	if err != nil {
		return f4PipelineResult{}, err
	}
	bom, content := tools.StripBOM(rawContent)
	ending := tools.DetectLineEnding(content)
	applied, err := tools.ApplyEditsToNormalizedContent(tools.NormalizeToLF(content), fixtureCase.Edits, path)
	if err != nil {
		return f4PipelineResult{}, err
	}
	diff := tools.GenerateDiffString(applied.BaseContent, applied.NewContent, 4)
	patch, err := tools.GenerateUnifiedPatch(path, applied.BaseContent, applied.NewContent, 4)
	if err != nil {
		return f4PipelineResult{}, err
	}
	return f4PipelineResult{
		FinalContent:     bom + tools.RestoreLineEndings(applied.NewContent, ending),
		BaseContent:      applied.BaseContent,
		NewContent:       applied.NewContent,
		Diff:             diff.Diff,
		FirstChangedLine: diff.FirstChangedLine,
		Patch:            patch,
	}, nil
}

func f4ContentAndPath(fixtureCase f4Case) (content, path string, err error) {
	content, err = f4RequiredString(fixtureCase.Content, "content", fixtureCase.Name)
	if err != nil {
		return "", "", err
	}
	path, err = f4RequiredString(fixtureCase.Path, "path", fixtureCase.Name)
	return content, path, err
}

func f4OldAndNew(fixtureCase f4Case) (oldText, newText string, err error) {
	oldText, err = f4RequiredString(fixtureCase.OldText, "oldText", fixtureCase.Name)
	if err != nil {
		return "", "", err
	}
	newText, err = f4RequiredString(fixtureCase.NewText, "newText", fixtureCase.Name)
	return oldText, newText, err
}

func f4RequiredString(value *string, field, name string) (string, error) {
	if value == nil {
		return "", fmt.Errorf("F4 case %q is missing %s", name, field)
	}
	return *value, nil
}

func f4ContextLines(fixtureCase f4Case) int {
	if fixtureCase.ContextLines == nil {
		return 4
	}
	return *fixtureCase.ContextLines
}
