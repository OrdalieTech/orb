package harness

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/OrdalieTech/pi-go/agent"
	"github.com/OrdalieTech/pi-go/ai"
)

const BranchSummaryPreamble = `The user explored a different conversation branch before returning here.
Summary of that exploration:

`

const BranchSummaryPrompt = `Create a structured summary of this conversation branch for context when returning later.

Use this EXACT format:

## Goal
[What was the user trying to accomplish in this branch?]

## Constraints & Preferences
- [Any constraints, preferences, or requirements mentioned]
- [Or "(none)" if none were mentioned]

## Progress
### Done
- [x] [Completed tasks/changes]

### In Progress
- [ ] [Work that was started but not finished]

### Blocked
- [Issues preventing progress, if any]

## Key Decisions
- **[Decision]**: [Brief rationale]

## Next Steps
1. [What should happen next to continue this work]

Keep each section concise. Preserve exact file paths, function names, and error messages.`

func CollectEntriesForBranchSummary(entries []SessionEntry, oldLeafID *string, targetID string) (CollectEntriesResult, error) {
	if oldLeafID == nil {
		return CollectEntriesResult{}, nil
	}
	index := make(map[string]SessionEntry, len(entries))
	for _, entry := range entries {
		index[entry.ID] = entry
	}
	oldPath := make(map[string]struct{})
	for current := oldLeafID; current != nil; {
		entry, ok := index[*current]
		if !ok {
			return CollectEntriesResult{}, entryNotFoundError(*current)
		}
		oldPath[entry.ID] = struct{}{}
		current = entry.ParentID
	}
	targetPath := make([]SessionEntry, 0)
	current := &targetID
	for current != nil {
		entry, ok := index[*current]
		if !ok {
			return CollectEntriesResult{}, entryNotFoundError(*current)
		}
		targetPath = append(targetPath, entry)
		current = entry.ParentID
	}
	var common *string
	for _, entry := range targetPath {
		if _, ok := oldPath[entry.ID]; ok {
			value := entry.ID
			common = &value
			break
		}
	}
	selected := make([]SessionEntry, 0)
	current = oldLeafID
	for current != nil && (common == nil || *current != *common) {
		entry, ok := index[*current]
		if !ok {
			return CollectEntriesResult{}, entryNotFoundError(*current)
		}
		selected = append(selected, entry)
		current = entry.ParentID
	}
	for left, right := 0, len(selected)-1; left < right; left, right = left+1, right-1 {
		selected[left], selected[right] = selected[right], selected[left]
	}
	return CollectEntriesResult{Entries: selected, CommonAncestorID: common}, nil
}

//nolint:staticcheck // Upstream SessionError capitalization is observable.
func entryNotFoundError(id string) error { return fmt.Errorf("Entry %s not found", id) }

func PrepareBranchEntries(entries []SessionEntry, tokenBudget float64) BranchPreparation {
	operations := newFileOperations()
	for _, entry := range entries {
		if entry.Type == "branch_summary" && !entry.FromHook {
			mergeDetails(&operations, entry.Details)
		}
	}
	messages := agent.AgentMessages{}
	var total int64
	for index := len(entries) - 1; index >= 0; index-- {
		message := entryMessage(entries[index], true)
		if message == nil || (entries[index].Type == "message" && messageRole(message) == "toolResult") {
			continue
		}
		extractFileOperations(message, &operations)
		tokens := EstimateTokens(message)
		if tokenBudget > 0 && float64(total+tokens) > tokenBudget {
			if (entries[index].Type == "compaction" || entries[index].Type == "branch_summary") && float64(total) < tokenBudget*0.9 {
				messages = append(agent.AgentMessages{message}, messages...)
				total += tokens
			}
			break
		}
		messages = append(agent.AgentMessages{message}, messages...)
		total += tokens
	}
	return BranchPreparation{Messages: messages, FileOps: operations, TotalTokens: total}
}

//nolint:staticcheck // BranchSummaryError messages match upstream capitalization.
func GenerateBranchSummary(ctx context.Context, entries []SessionEntry, options GenerateBranchSummaryOptions) (*BranchSummaryResult, error) {
	reserve := int64(16384)
	if options.ReserveTokens != nil {
		reserve = *options.ReserveTokens
	}
	contextWindow := float64(128000)
	if options.Model != nil && options.Model.ContextWindow != 0 {
		contextWindow = options.Model.ContextWindow
	}
	prepared := PrepareBranchEntries(entries, contextWindow-float64(reserve))
	if len(prepared.Messages) == 0 {
		return &BranchSummaryResult{Summary: "No content to summarize", ReadFiles: []string{}, ModifiedFiles: []string{}}, nil
	}
	instructions := BranchSummaryPrompt
	if options.ReplaceInstructions && options.CustomInstructions != "" {
		instructions = options.CustomInstructions
	} else if options.CustomInstructions != "" {
		instructions += "\n\nAdditional focus: " + options.CustomInstructions
	}
	prompt := "<conversation>\n" + SerializeConversation(prepared.Messages) + "\n</conversation>\n\n" + instructions
	maxTokens := float64(2048)
	if options.Model == nil || options.Complete == nil {
		return nil, errors.New("Branch summary failed: no model or completion function")
	}
	system := SummarizationSystemPrompt
	request := ai.Context{
		SystemPrompt: &system,
		Messages:     ai.MessageList{&ai.UserMessage{Content: ai.NewUserContent(&ai.TextContent{Text: prompt})}},
	}
	response, err := options.Complete(ctx, options.Model, request, &ai.SimpleStreamOptions{StreamOptions: ai.StreamOptions{MaxTokens: &maxTokens}})
	if err != nil {
		return nil, fmt.Errorf("Branch summary failed: %w", err)
	}
	if response.StopReason == ai.StopReasonAborted {
		if response.ErrorMessage != nil && *response.ErrorMessage != "" {
			return nil, errors.New(*response.ErrorMessage)
		}
		return nil, errors.New("Branch summary aborted")
	}
	if response.StopReason == ai.StopReasonError {
		message := "Unknown error"
		if response.ErrorMessage != nil && *response.ErrorMessage != "" {
			message = *response.ErrorMessage
		}
		return nil, errors.New("Branch summary failed: " + message)
	}
	texts := make([]string, 0, len(response.Content))
	for _, block := range response.Content {
		if text, ok := block.(*ai.TextContent); ok {
			texts = append(texts, text.Text)
		}
	}
	readFiles, modifiedFiles := computeFileLists(prepared.FileOps)
	summary := BranchSummaryPreamble + strings.Join(texts, "\n")
	summary += FormatFileOperations(readFiles, modifiedFiles)
	if summary == "" {
		summary = "No summary generated"
	}
	usage := response.Usage
	return &BranchSummaryResult{Summary: summary, Usage: &usage, ReadFiles: readFiles, ModifiedFiles: modifiedFiles}, nil
}
