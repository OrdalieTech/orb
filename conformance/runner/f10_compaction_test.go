package runner_test

import (
	"context"
	"encoding/json"
	"reflect"
	"sort"
	"testing"

	"github.com/OrdalieTech/pi-go/agent"
	"github.com/OrdalieTech/pi-go/agent/harness"
	"github.com/OrdalieTech/pi-go/ai"
	"github.com/OrdalieTech/pi-go/conformance/runner"
)

type f10Fixture struct {
	SchemaVersion      int                    `json:"schemaVersion"`
	TokenCases         []f10TokenCase         `json:"tokenCases"`
	ConversationCases  []f10ConversationCase  `json:"conversationCases"`
	ContextCases       []f10ContextCase       `json:"contextCases"`
	CutCases           []f10CutCase           `json:"cutCases"`
	PrepareCases       []f10PrepareCase       `json:"prepareCases"`
	BranchCases        []f10BranchCase        `json:"branchCases"`
	SummaryPromptCases []f10SummaryPromptCase `json:"summaryPromptCases"`
	BranchPromptCases  []f10BranchPromptCase  `json:"branchPromptCases"`
	CompactPromptCases []f10CompactPromptCase `json:"compactPromptCases"`
}

type f10TokenCase struct {
	Name     string          `json:"name"`
	Message  json.RawMessage `json:"message"`
	Expected int64           `json:"expected"`
}

type f10ConversationCase struct {
	Name     string            `json:"name"`
	Messages []json.RawMessage `json:"messages"`
	Expected string            `json:"expected"`
}

type f10ContextCase struct {
	Name     string                       `json:"name"`
	Messages []json.RawMessage            `json:"messages"`
	Expected harness.ContextUsageEstimate `json:"expected"`
}

type f10Entry struct {
	Type             string          `json:"type"`
	ID               string          `json:"id"`
	ParentID         *string         `json:"parentId"`
	Timestamp        string          `json:"timestamp"`
	Message          json.RawMessage `json:"message"`
	Summary          string          `json:"summary"`
	FirstKeptEntryID string          `json:"firstKeptEntryId"`
	TokensBefore     int64           `json:"tokensBefore"`
	Details          json.RawMessage `json:"details"`
	FromHook         bool            `json:"fromHook"`
	FromID           string          `json:"fromId"`
	CustomType       string          `json:"customType"`
	Content          json.RawMessage `json:"content"`
	Display          bool            `json:"display"`
}

type f10CutCase struct {
	Name             string                 `json:"name"`
	Entries          []f10Entry             `json:"entries"`
	StartIndex       int                    `json:"startIndex"`
	EndIndex         int                    `json:"endIndex"`
	KeepRecentTokens int64                  `json:"keepRecentTokens"`
	Expected         harness.CutPointResult `json:"expected"`
}

type f10PrepareCase struct {
	Name     string                     `json:"name"`
	Entries  []f10Entry                 `json:"entries"`
	Settings harness.CompactionSettings `json:"settings"`
	Expected *f10PrepareExpected        `json:"expected"`
}

type f10PrepareExpected struct {
	FirstKeptEntryID    string   `json:"firstKeptEntryId"`
	SummaryRoles        []string `json:"summaryRoles"`
	PrefixRoles         []string `json:"prefixRoles"`
	SummaryConversation string   `json:"summaryConversation"`
	PrefixConversation  string   `json:"prefixConversation"`
	IsSplitTurn         bool     `json:"isSplitTurn"`
	TokensBefore        int64    `json:"tokensBefore"`
	PreviousSummary     *string  `json:"previousSummary"`
	ReadFiles           []string `json:"readFiles"`
	ModifiedFiles       []string `json:"modifiedFiles"`
}

type f10BranchCase struct {
	Name        string            `json:"name"`
	Entries     []f10Entry        `json:"entries"`
	OldLeafID   *string           `json:"oldLeafId"`
	TargetID    string            `json:"targetId"`
	TokenBudget int64             `json:"tokenBudget"`
	Expected    f10BranchExpected `json:"expected"`
}

type f10BranchExpected struct {
	EntryIDs         []string `json:"entryIds"`
	CommonAncestorID *string  `json:"commonAncestorId"`
	Roles            []string `json:"roles"`
	Conversation     string   `json:"conversation"`
	TotalTokens      int64    `json:"totalTokens"`
	ReadFiles        []string `json:"readFiles"`
	ModifiedFiles    []string `json:"modifiedFiles"`
}

type f10SummaryPromptCase struct {
	Input    f10SummaryPromptInput `json:"input"`
	Expected f10PromptExpected     `json:"expected"`
}

type f10SummaryPromptInput struct {
	Name               string            `json:"name"`
	Messages           []json.RawMessage `json:"messages"`
	ReserveTokens      int64             `json:"reserveTokens"`
	CustomInstructions string            `json:"customInstructions"`
	PreviousSummarySet bool              `json:"previousSummarySet"`
	PreviousSummary    string            `json:"previousSummary"`
	ThinkingLevel      string            `json:"thinkingLevel"`
}

type f10BranchPromptCase struct {
	Name     string                  `json:"name"`
	Input    f10BranchPromptInput    `json:"input"`
	Expected f10BranchPromptExpected `json:"expected"`
}

type f10BranchPromptInput struct {
	Entries             []f10Entry `json:"entries"`
	ReserveTokens       int64      `json:"reserveTokens"`
	CustomInstructions  string     `json:"customInstructions"`
	ReplaceInstructions bool       `json:"replaceInstructions"`
}

type f10BranchPromptExpected struct {
	Captured f10CapturedRequest          `json:"captured"`
	Output   harness.BranchSummaryResult `json:"output"`
}

type f10CompactPromptCase struct {
	Name     string                   `json:"name"`
	Input    f10CompactPromptInput    `json:"input"`
	Expected f10CompactPromptExpected `json:"expected"`
}

type f10CompactPromptInput struct {
	FirstKeptEntryID    string                     `json:"firstKeptEntryId"`
	MessagesToSummarize []json.RawMessage          `json:"messagesToSummarize"`
	TurnPrefixMessages  []json.RawMessage          `json:"turnPrefixMessages"`
	IsSplitTurn         bool                       `json:"isSplitTurn"`
	TokensBefore        int64                      `json:"tokensBefore"`
	PreviousSummary     *string                    `json:"previousSummary"`
	Settings            harness.CompactionSettings `json:"settings"`
}

type f10CompactPromptExpected struct {
	Captured []f10CapturedRequest `json:"captured"`
	Output   f10CompactionOutput  `json:"output"`
}

type f10CompactionOutput struct {
	Summary          string                    `json:"summary"`
	FirstKeptEntryID string                    `json:"firstKeptEntryId"`
	TokensBefore     int64                     `json:"tokensBefore"`
	Details          harness.CompactionDetails `json:"details"`
}

type f10PromptExpected struct {
	Captured f10CapturedRequest `json:"captured"`
	Output   string             `json:"output"`
}

type f10CapturedRequest struct {
	Context struct {
		SystemPrompt string `json:"systemPrompt"`
		Messages     []struct {
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
		} `json:"messages"`
	} `json:"context"`
	Options struct {
		MaxTokens float64 `json:"maxTokens"`
		Reasoning string  `json:"reasoning"`
		Signal    string  `json:"signal"`
	} `json:"options"`
}

type f10ActualCapture struct {
	SystemPrompt string
	Prompt       string
	MaxTokens    float64
	Reasoning    string
}

func TestF10TokenAndConversationFixturesMatchUpstream(t *testing.T) {
	fixture := loadF10Fixture(t)
	for _, fixtureCase := range fixture.TokenCases {
		fixtureCase := fixtureCase
		t.Run("tokens/"+fixtureCase.Name, func(t *testing.T) {
			got := harness.EstimateTokens(f10Message(t, fixtureCase.Message))
			if got != fixtureCase.Expected {
				t.Fatalf("token estimate = %d, want %d", got, fixtureCase.Expected)
			}
		})
	}
	for _, fixtureCase := range fixture.ConversationCases {
		fixtureCase := fixtureCase
		t.Run("conversation/"+fixtureCase.Name, func(t *testing.T) {
			got := harness.SerializeConversation(f10Messages(t, fixtureCase.Messages))
			if got != fixtureCase.Expected {
				t.Fatalf("conversation mismatch:\n%s", runner.ByteDiff([]byte(fixtureCase.Expected), []byte(got)))
			}
		})
	}
}

func TestF10ContextAndCutBoundariesMatchUpstream(t *testing.T) {
	fixture := loadF10Fixture(t)
	for _, fixtureCase := range fixture.ContextCases {
		fixtureCase := fixtureCase
		t.Run("context/"+fixtureCase.Name, func(t *testing.T) {
			got := harness.EstimateContextTokens(f10Messages(t, fixtureCase.Messages))
			if !reflect.DeepEqual(got, fixtureCase.Expected) {
				t.Fatalf("context estimate = %+v, want %+v", got, fixtureCase.Expected)
			}
		})
	}
	for _, fixtureCase := range fixture.CutCases {
		fixtureCase := fixtureCase
		t.Run("cut/"+fixtureCase.Name, func(t *testing.T) {
			got := harness.FindCutPoint(f10Entries(t, fixtureCase.Entries), fixtureCase.StartIndex, fixtureCase.EndIndex, fixtureCase.KeepRecentTokens)
			if got != fixtureCase.Expected {
				t.Fatalf("cut point = %+v, want %+v", got, fixtureCase.Expected)
			}
		})
	}
}

func TestF10CompactionPreparationMatchesUpstream(t *testing.T) {
	fixture := loadF10Fixture(t)
	for _, fixtureCase := range fixture.PrepareCases {
		fixtureCase := fixtureCase
		t.Run(fixtureCase.Name, func(t *testing.T) {
			prepared, err := harness.PrepareCompaction(f10Entries(t, fixtureCase.Entries), fixtureCase.Settings)
			if err != nil {
				t.Fatalf("prepare compaction: %v", err)
			}
			if fixtureCase.Expected == nil {
				if prepared != nil {
					t.Fatalf("prepared = %+v, want nil", prepared)
				}
				return
			}
			if prepared == nil {
				t.Fatal("prepared = nil")
			}
			readFiles, modifiedFiles := f10FileLists(prepared.FileOps)
			got := f10PrepareExpected{
				FirstKeptEntryID: prepared.FirstKeptEntryID, SummaryRoles: f10Roles(t, prepared.MessagesToSummarize),
				PrefixRoles: f10Roles(t, prepared.TurnPrefixMessages), SummaryConversation: harness.SerializeConversation(prepared.MessagesToSummarize),
				PrefixConversation: harness.SerializeConversation(prepared.TurnPrefixMessages), IsSplitTurn: prepared.IsSplitTurn,
				TokensBefore: prepared.TokensBefore, PreviousSummary: prepared.PreviousSummary,
				ReadFiles: readFiles, ModifiedFiles: modifiedFiles,
			}
			if !reflect.DeepEqual(got, *fixtureCase.Expected) {
				t.Fatalf("preparation mismatch\nwant: %+v\n got: %+v", *fixtureCase.Expected, got)
			}
		})
	}
}

func TestF10BranchSelectionMatchesUpstream(t *testing.T) {
	fixture := loadF10Fixture(t)
	for _, fixtureCase := range fixture.BranchCases {
		fixtureCase := fixtureCase
		t.Run(fixtureCase.Name, func(t *testing.T) {
			collected, err := harness.CollectEntriesForBranchSummary(f10Entries(t, fixtureCase.Entries), fixtureCase.OldLeafID, fixtureCase.TargetID)
			if err != nil {
				t.Fatalf("collect entries: %v", err)
			}
			ids := make([]string, len(collected.Entries))
			for index, entry := range collected.Entries {
				ids[index] = entry.ID
			}
			prepared := harness.PrepareBranchEntries(collected.Entries, float64(fixtureCase.TokenBudget))
			readFiles, modifiedFiles := f10FileLists(prepared.FileOps)
			got := f10BranchExpected{
				EntryIDs: ids, CommonAncestorID: collected.CommonAncestorID, Roles: f10Roles(t, prepared.Messages),
				Conversation: harness.SerializeConversation(prepared.Messages), TotalTokens: prepared.TotalTokens,
				ReadFiles: readFiles, ModifiedFiles: modifiedFiles,
			}
			if !reflect.DeepEqual(got, fixtureCase.Expected) {
				t.Fatalf("branch selection mismatch\nwant: %+v\n got: %+v", fixtureCase.Expected, got)
			}
		})
	}
}

func TestF10SummaryPromptsMatchUpstream(t *testing.T) {
	fixture := loadF10Fixture(t)
	model := f10Model()
	for _, fixtureCase := range fixture.SummaryPromptCases {
		fixtureCase := fixtureCase
		t.Run(fixtureCase.Input.Name, func(t *testing.T) {
			var captured f10ActualCapture
			complete := f10Completion(&captured, "summary output")
			var previousSummary *string
			if fixtureCase.Input.PreviousSummarySet {
				value := fixtureCase.Input.PreviousSummary
				previousSummary = &value
			}
			got, err := harness.GenerateSummary(context.Background(), f10Messages(t, fixtureCase.Input.Messages), model, complete,
				fixtureCase.Input.ReserveTokens, fixtureCase.Input.CustomInstructions, previousSummary, ai.ModelThinkingLevel(fixtureCase.Input.ThinkingLevel))
			if err != nil {
				t.Fatalf("generate summary: %v", err)
			}
			assertF10Capture(t, captured, fixtureCase.Expected.Captured)
			if got != fixtureCase.Expected.Output {
				t.Fatalf("summary output = %q, want %q", got, fixtureCase.Expected.Output)
			}
		})
	}
}

func TestF10BranchAndSplitTurnPromptsMatchUpstream(t *testing.T) {
	fixture := loadF10Fixture(t)
	model := f10Model()
	for _, fixtureCase := range fixture.BranchPromptCases {
		fixtureCase := fixtureCase
		t.Run("branch/"+fixtureCase.Name, func(t *testing.T) {
			var captured f10ActualCapture
			got, err := harness.GenerateBranchSummary(context.Background(), f10Entries(t, fixtureCase.Input.Entries), harness.GenerateBranchSummaryOptions{
				Model: model, Complete: f10Completion(&captured, "branch output"), CustomInstructions: fixtureCase.Input.CustomInstructions,
				ReplaceInstructions: fixtureCase.Input.ReplaceInstructions, ReserveTokens: &fixtureCase.Input.ReserveTokens,
			})
			if err != nil {
				t.Fatalf("generate branch summary: %v", err)
			}
			assertF10Capture(t, captured, fixtureCase.Expected.Captured)
			if !reflect.DeepEqual(*got, fixtureCase.Expected.Output) {
				t.Fatalf("branch output = %+v, want %+v", *got, fixtureCase.Expected.Output)
			}
		})
	}

	for _, fixtureCase := range fixture.CompactPromptCases {
		fixtureCase := fixtureCase
		t.Run("compact/"+fixtureCase.Name, func(t *testing.T) {
			captures := make([]f10ActualCapture, 0, 2)
			outputs := []string{"history summary", "prefix summary"}
			complete := func(_ context.Context, _ *ai.Model, request ai.Context, options *ai.SimpleStreamOptions) (*ai.AssistantMessage, error) {
				capture := f10Capture(request, options)
				captures = append(captures, capture)
				return f10Response(outputs[len(captures)-1]), nil
			}
			prepared := &harness.CompactionPreparation{
				FirstKeptEntryID: fixtureCase.Input.FirstKeptEntryID, MessagesToSummarize: f10Messages(t, fixtureCase.Input.MessagesToSummarize),
				TurnPrefixMessages: f10Messages(t, fixtureCase.Input.TurnPrefixMessages), IsSplitTurn: fixtureCase.Input.IsSplitTurn,
				TokensBefore: fixtureCase.Input.TokensBefore, PreviousSummary: fixtureCase.Input.PreviousSummary,
				FileOps:  harness.FileOperations{Read: map[string]struct{}{}, Written: map[string]struct{}{}, Edited: map[string]struct{}{}},
				Settings: fixtureCase.Input.Settings,
			}
			got, err := harness.Compact(context.Background(), prepared, model, complete, "", ai.ModelThinkingHigh)
			if err != nil {
				t.Fatalf("compact: %v", err)
			}
			if len(captures) != len(fixtureCase.Expected.Captured) {
				t.Fatalf("captures = %d, want %d", len(captures), len(fixtureCase.Expected.Captured))
			}
			for index := range captures {
				assertF10Capture(t, captures[index], fixtureCase.Expected.Captured[index])
			}
			output := f10CompactionOutput{Summary: got.Summary, FirstKeptEntryID: got.FirstKeptEntryID, TokensBefore: got.TokensBefore, Details: got.Details}
			if !reflect.DeepEqual(output, fixtureCase.Expected.Output) {
				t.Fatalf("compaction output = %+v, want %+v", output, fixtureCase.Expected.Output)
			}
		})
	}
}

func loadF10Fixture(t testing.TB) f10Fixture {
	t.Helper()
	manifest := runner.LoadManifest(t, "F10")
	if manifest.Family != "F10" || manifest.Generator != "conformance/extract/f10-compaction.ts" {
		t.Fatalf("unexpected F10 manifest: %+v", manifest)
	}
	var fixture f10Fixture
	runner.LoadJSON(t, "F10", "cases.json", &fixture)
	if fixture.SchemaVersion != 1 || len(fixture.TokenCases) != 8 || len(fixture.CutCases) != 4 || len(fixture.SummaryPromptCases) != 3 {
		t.Fatalf("unexpected F10 fixture header: version=%d token=%d cut=%d prompts=%d", fixture.SchemaVersion, len(fixture.TokenCases), len(fixture.CutCases), len(fixture.SummaryPromptCases))
	}
	return fixture
}

func f10Message(t testing.TB, raw json.RawMessage) agent.AgentMessage {
	t.Helper()
	if message, err := ai.UnmarshalMessage(raw); err == nil {
		return message
	}
	var envelope struct {
		Role string `json:"role"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		t.Fatalf("decode message role: %v", err)
	}
	switch envelope.Role {
	case "custom":
		var message harness.CustomMessage
		if err := json.Unmarshal(raw, &message); err != nil {
			t.Fatalf("decode custom message: %v", err)
		}
		return &message
	case "bashExecution":
		var message harness.BashExecutionMessage
		if err := json.Unmarshal(raw, &message); err != nil {
			t.Fatalf("decode bash message: %v", err)
		}
		return &message
	case "branchSummary", "compactionSummary":
		var message harness.SummaryMessage
		if err := json.Unmarshal(raw, &message); err != nil {
			t.Fatalf("decode summary message: %v", err)
		}
		return &message
	default:
		t.Fatalf("unsupported fixture message role %q", envelope.Role)
		return nil
	}
}

func f10Messages(t testing.TB, raw []json.RawMessage) agent.AgentMessages {
	t.Helper()
	messages := make(agent.AgentMessages, len(raw))
	for index, message := range raw {
		messages[index] = f10Message(t, message)
	}
	return messages
}

func f10Entries(t testing.TB, entries []f10Entry) []harness.SessionEntry {
	t.Helper()
	converted := make([]harness.SessionEntry, len(entries))
	for index, entry := range entries {
		var message agent.AgentMessage
		if len(entry.Message) > 0 {
			message = f10Message(t, entry.Message)
		}
		var details any
		if len(entry.Details) > 0 {
			if err := json.Unmarshal(entry.Details, &details); err != nil {
				t.Fatalf("decode entry details: %v", err)
			}
		}
		var content any
		if len(entry.Content) > 0 {
			if err := json.Unmarshal(entry.Content, &content); err != nil {
				t.Fatalf("decode entry content: %v", err)
			}
		}
		converted[index] = harness.SessionEntry{
			Type: entry.Type, ID: entry.ID, ParentID: entry.ParentID, Timestamp: entry.Timestamp, Message: message,
			Summary: entry.Summary, FirstKeptEntryID: entry.FirstKeptEntryID, TokensBefore: float64(entry.TokensBefore),
			Details: details, FromHook: entry.FromHook, FromID: entry.FromID, CustomType: entry.CustomType,
			Content: content, Display: entry.Display,
		}
	}
	return converted
}

func f10Roles(t testing.TB, messages agent.AgentMessages) []string {
	t.Helper()
	roles := make([]string, len(messages))
	for index, message := range messages {
		encoded, err := ai.Marshal(message)
		if err != nil {
			t.Fatalf("marshal message role: %v", err)
		}
		var envelope struct {
			Role string `json:"role"`
		}
		if err := json.Unmarshal(encoded, &envelope); err != nil {
			t.Fatalf("decode marshaled role: %v", err)
		}
		roles[index] = envelope.Role
	}
	return roles
}

func f10FileLists(operations harness.FileOperations) ([]string, []string) {
	modified := make(map[string]struct{}, len(operations.Written)+len(operations.Edited))
	for file := range operations.Written {
		modified[file] = struct{}{}
	}
	for file := range operations.Edited {
		modified[file] = struct{}{}
	}
	readFiles := make([]string, 0, len(operations.Read))
	for file := range operations.Read {
		if _, changed := modified[file]; !changed {
			readFiles = append(readFiles, file)
		}
	}
	modifiedFiles := make([]string, 0, len(modified))
	for file := range modified {
		modifiedFiles = append(modifiedFiles, file)
	}
	sort.Strings(readFiles)
	sort.Strings(modifiedFiles)
	return readFiles, modifiedFiles
}

func f10Model() *ai.Model {
	return &ai.Model{
		ID: "fixture-model", Name: "Fixture", API: ai.APIOpenAIResponses, Provider: "fixture",
		BaseURL: "https://fixture.invalid", Reasoning: true, ContextWindow: 4096, MaxTokens: 300,
	}
}

func f10Completion(captured *f10ActualCapture, output string) harness.CompleteFunc {
	return func(_ context.Context, _ *ai.Model, request ai.Context, options *ai.SimpleStreamOptions) (*ai.AssistantMessage, error) {
		*captured = f10Capture(request, options)
		return f10Response(output), nil
	}
}

func f10Capture(request ai.Context, options *ai.SimpleStreamOptions) f10ActualCapture {
	captured := f10ActualCapture{}
	if request.SystemPrompt != nil {
		captured.SystemPrompt = *request.SystemPrompt
	}
	if len(request.Messages) > 0 {
		if user, ok := request.Messages[0].(*ai.UserMessage); ok && len(user.Content.Blocks) > 0 {
			if text, ok := user.Content.Blocks[0].(*ai.TextContent); ok {
				captured.Prompt = text.Text
			}
		}
	}
	if options != nil {
		if options.MaxTokens != nil {
			captured.MaxTokens = *options.MaxTokens
		}
		if options.Reasoning != nil {
			captured.Reasoning = string(*options.Reasoning)
		}
	}
	return captured
}

func assertF10Capture(t testing.TB, got f10ActualCapture, expected f10CapturedRequest) {
	t.Helper()
	want := f10ActualCapture{
		SystemPrompt: expected.Context.SystemPrompt,
		MaxTokens:    expected.Options.MaxTokens,
		Reasoning:    expected.Options.Reasoning,
	}
	if len(expected.Context.Messages) > 0 && len(expected.Context.Messages[0].Content) > 0 {
		want.Prompt = expected.Context.Messages[0].Content[0].Text
	}
	if got != want {
		if got.Prompt != want.Prompt {
			t.Fatalf("prompt mismatch:\n%s", runner.ByteDiff([]byte(want.Prompt), []byte(got.Prompt)))
		}
		t.Fatalf("capture = %+v, want %+v", got, want)
	}
}

func f10Response(text string) *ai.AssistantMessage {
	return &ai.AssistantMessage{
		Content:    ai.AssistantContent{&ai.TextContent{Text: text}},
		StopReason: ai.StopReasonStop,
	}
}
