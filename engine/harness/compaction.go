package harness

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode/utf16"

	"github.com/OrdalieTech/orb/ai"
	"github.com/OrdalieTech/orb/engine"
)

const estimatedImageChars int64 = 4800

const branchSummaryPrefix = `The following is a summary of a branch that this conversation came back from:

<summary>
`

const branchSummarySuffix = `</summary>`

const compactionSummaryPrefix = `The conversation history before this point was compacted into the following summary:

<summary>
`

const compactionSummarySuffix = `
</summary>`

const SummarizationSystemPrompt = `You are a context summarization assistant. Your task is to read a conversation between a user and an AI assistant, then produce a structured summary following the exact format specified.

Do NOT continue the conversation. Do NOT respond to any questions in the conversation. ONLY output the structured summary.`

const SummarizationPrompt = `The messages above are a conversation to summarize. Create a structured context checkpoint summary that another LLM will use to continue the work.

Use this EXACT format:

## Goal
[What is the user trying to accomplish? Can be multiple items if the session covers different tasks.]

## Constraints & Preferences
- [Any constraints, preferences, or requirements mentioned by user]
- [Or "(none)" if none were mentioned]

## Progress
### Done
- [x] [Completed tasks/changes]

### In Progress
- [ ] [Current work]

### Blocked
- [Issues preventing progress, if any]

## Key Decisions
- **[Decision]**: [Brief rationale]

## Next Steps
1. [Ordered list of what should happen next]

## Critical Context
- [Any data, examples, or references needed to continue]
- [Or "(none)" if not applicable]

Keep each section concise. Preserve exact file paths, function names, and error messages.`

const UpdateSummarizationPrompt = `The messages above are NEW conversation messages to incorporate into the existing summary provided in <previous-summary> tags.

Update the existing structured summary with new information. RULES:
- PRESERVE all existing information from the previous summary
- ADD new progress, decisions, and context from the new messages
- UPDATE the Progress section: move items from "In Progress" to "Done" when completed
- UPDATE "Next Steps" based on what was accomplished
- PRESERVE exact file paths, function names, and error messages
- If something is no longer relevant, you may remove it

Use this EXACT format:

## Goal
[Preserve existing goals, add new ones if the task expanded]

## Constraints & Preferences
- [Preserve existing, add new ones discovered]

## Progress
### Done
- [x] [Include previously done items AND newly completed items]

### In Progress
- [ ] [Current work - update based on progress]

### Blocked
- [Current blockers - remove if resolved]

## Key Decisions
- **[Decision]**: [Brief rationale] (preserve all previous, add new)

## Next Steps
1. [Update based on current state]

## Critical Context
- [Preserve important context, add new if needed]

Keep each section concise. Preserve exact file paths, function names, and error messages.`

const TurnPrefixSummarizationPrompt = `This is the PREFIX of a turn that was too large to keep. The SUFFIX (recent work) is retained.

Summarize the prefix to provide context for the retained suffix:

## Original Request
[What did the user ask for in this turn?]

## Early Progress
- [Key decisions and work done in the prefix]

## Context for Suffix
- [Information needed to understand the retained recent work]

Be concise. Focus on what's needed to understand the kept suffix.`

func CalculateContextTokens(usage ai.Usage) int64 {
	if usage.TotalTokens != 0 {
		return usage.TotalTokens
	}
	return usage.Input + usage.Output + usage.CacheRead + usage.CacheWrite
}

func EstimateContextTokens(messages engine.AgentMessages) ContextUsageEstimate {
	lastIndex := -1
	var usage *ai.Usage
	for index := len(messages) - 1; index >= 0; index-- {
		if found := assistantUsage(messages[index]); found != nil {
			lastIndex = index
			usage = found
			break
		}
	}
	if usage == nil {
		var estimated int64
		for _, message := range messages {
			estimated += EstimateTokens(message)
		}
		return ContextUsageEstimate{Tokens: estimated, TrailingTokens: estimated}
	}
	usageTokens := CalculateContextTokens(*usage)
	var trailing int64
	for _, message := range messages[lastIndex+1:] {
		trailing += EstimateTokens(message)
	}
	indexCopy := lastIndex
	return ContextUsageEstimate{
		Tokens: usageTokens + trailing, UsageTokens: usageTokens,
		TrailingTokens: trailing, LastUsageIndex: &indexCopy,
	}
}

func ShouldCompact(contextTokens int64, contextWindow float64, settings CompactionSettings) bool {
	return settings.Enabled && float64(contextTokens) > contextWindow-float64(settings.ReserveTokens)
}

func EstimateTokens(message engine.AgentMessage) int64 {
	switch typed := message.(type) {
	case *ai.UserMessage:
		return ceilQuarter(userContentChars(typed.Content))
	case ai.UserMessage:
		return ceilQuarter(userContentChars(typed.Content))
	case *ai.AssistantMessage:
		return ceilQuarter(assistantContentChars(typed.Content))
	case ai.AssistantMessage:
		return ceilQuarter(assistantContentChars(typed.Content))
	case *ai.ToolResultMessage:
		return ceilQuarter(toolResultChars(typed.Content))
	case ai.ToolResultMessage:
		return ceilQuarter(toolResultChars(typed.Content))
	case CustomMessage:
		return ceilQuarter(contentChars(typed.Content))
	case *CustomMessage:
		return ceilQuarter(contentChars(typed.Content))
	case BashExecutionMessage:
		return ceilQuarter(jsLength(typed.Command) + jsLength(typed.Output))
	case *BashExecutionMessage:
		return ceilQuarter(jsLength(typed.Command) + jsLength(typed.Output))
	case SummaryMessage:
		return ceilQuarter(jsLength(typed.Summary))
	case *SummaryMessage:
		return ceilQuarter(jsLength(typed.Summary))
	case json.RawMessage:
		return estimateRawMessage(typed)
	case []byte:
		return estimateRawMessage(typed)
	default:
		encoded, err := ai.Marshal(message)
		if err != nil {
			return 0
		}
		return estimateRawMessage(encoded)
	}
}

func FindTurnStartIndex(entries []SessionEntry, entryIndex, startIndex int) int {
	for index := entryIndex; index >= startIndex; index-- {
		if isTurnStartEntry(entries[index]) {
			return index
		}
	}
	return -1
}

func isTurnStartEntry(entry SessionEntry) bool {
	if entry.Type == "compaction" {
		return false
	}
	switch messageRole(entryMessage(entry, false)) {
	case "user", "bashExecution", "custom", "branchSummary", "compactionSummary":
		return true
	}
	return false
}

func FindCutPoint(entries []SessionEntry, startIndex, endIndex int, keepRecentTokens int64) CutPointResult {
	cutPoints := validCutPoints(entries, startIndex, endIndex)
	if len(cutPoints) == 0 {
		return CutPointResult{FirstKeptEntryIndex: startIndex, TurnStartIndex: -1}
	}
	var accumulated int64
	cutIndex := cutPoints[0]
	for index := endIndex - 1; index >= startIndex; index-- {
		// Compaction entries count here: upstream weighs each entry through
		// sessionEntryToContextMessages, which projects them to a summary message.
		message := entryMessage(entries[index], true)
		if message == nil {
			continue
		}
		messageTokens := EstimateTokens(message)
		if messageTokens == 0 {
			continue
		}
		accumulated += messageTokens
		if accumulated >= keepRecentTokens {
			chosen := -1
			for _, candidate := range cutPoints {
				if candidate >= index {
					chosen = candidate
					break
				}
			}
			if chosen < 0 {
				// The newest messages alone exceed the recent budget (typically one large
				// tool result). Cut at the latest boundary before them so the turn still
				// folds instead of retaining the whole path and compacting nothing.
				for _, candidate := range cutPoints {
					if candidate > index {
						break
					}
					chosen = candidate
				}
			}
			if chosen >= 0 {
				cutIndex = chosen
			}
			break
		}
	}
	for cutIndex > startIndex {
		previous := entries[cutIndex-1]
		if previous.Type == "compaction" || entryMessage(previous, false) != nil {
			break
		}
		cutIndex--
	}
	startsTurn := isTurnStartEntry(entries[cutIndex])
	turnStart := -1
	if !startsTurn {
		turnStart = FindTurnStartIndex(entries, cutIndex, startIndex)
	}
	return CutPointResult{
		FirstKeptEntryIndex: cutIndex,
		TurnStartIndex:      turnStart,
		IsSplitTurn:         !startsTurn && turnStart != -1,
	}
}

//nolint:staticcheck // CompactionError messages match upstream capitalization.
func PrepareCompaction(pathEntries []SessionEntry, settings CompactionSettings) (*CompactionPreparation, error) {
	return prepareCompaction(pathEntries, settings, true)
}

// PrepareTreeCompaction prepares the canonical persisted session entries.
func PrepareTreeCompaction(pathEntries []SessionTreeEntry, settings CompactionSettings) (*CompactionPreparation, error) {
	entries := make([]SessionEntry, len(pathEntries))
	for index := range pathEntries {
		entries[index] = projectTreeEntry(pathEntries[index])
	}
	return prepareCompaction(entries, settings, true)
}

// PrepareLegacyCompaction retains the coding-agent compaction behavior, whose
// persisted session format has not adopted harness retained-tail checkpoints.
func PrepareLegacyCompaction(pathEntries []SessionEntry, settings CompactionSettings) (*CompactionPreparation, error) {
	return prepareCompaction(pathEntries, settings, false)
}

//nolint:staticcheck // CompactionError messages match upstream capitalization.
func prepareCompaction(pathEntries []SessionEntry, settings CompactionSettings, retainTail bool) (*CompactionPreparation, error) {
	if len(pathEntries) == 0 || pathEntries[len(pathEntries)-1].Type == "compaction" {
		return nil, nil
	}
	previousIndex := -1
	for index := len(pathEntries) - 1; index >= 0; index-- {
		if pathEntries[index].Type == "compaction" {
			previousIndex = index
			break
		}
	}
	boundaryStart := 0
	entries := pathEntries
	var previousSummary *string
	if previousIndex >= 0 {
		previous := pathEntries[previousIndex]
		summary := previous.Summary
		previousSummary = &summary
		if retainTail {
			// Harness >=0.84 replays the previous compaction's retainedTail as
			// virtual message entries instead of resolving firstKeptEntryId.
			virtual := make([]SessionEntry, len(previous.RetainedTail))
			for index, message := range previous.RetainedTail {
				virtual[index] = SessionEntry{Type: "message", ID: fmt.Sprintf("%s:retained:%d", previous.ID, index), Message: message}
			}
			entries = append(virtual, pathEntries[previousIndex+1:]...)
		} else {
			boundaryStart = previousIndex + 1
			for index := range pathEntries {
				if pathEntries[index].ID == previous.FirstKeptEntryID {
					boundaryStart = index
					break
				}
			}
		}
	}
	var cut CutPointResult
	compactionMessage := entryMessage
	if retainTail {
		cut = harnessFindCutPoint(entries, boundaryStart, len(entries), settings.KeepRecentTokens)
		// Harness getMessageFromEntryForCompaction projects an empty-summary
		// branch_summary; the coding-agent projection drops it.
		compactionMessage = harnessEntryMessage
	} else {
		cut = FindCutPoint(entries, boundaryStart, len(entries), settings.KeepRecentTokens)
		if cut.FirstKeptEntryIndex < 0 || cut.FirstKeptEntryIndex >= len(entries) || entries[cut.FirstKeptEntryIndex].ID == "" {
			// coding-agent returns undefined here and lets the caller report
			// "Nothing to compact" (compaction.ts:735); harness >=0.84 no
			// longer resolves a first kept entry id at all.
			return nil, nil
		}
	}
	historyEnd := cut.FirstKeptEntryIndex
	if cut.IsSplitTurn {
		historyEnd = cut.TurnStartIndex
	}
	messages := make(engine.AgentMessages, 0, historyEnd-boundaryStart)
	for index := boundaryStart; index < historyEnd; index++ {
		if message := compactionMessage(entries[index], false); message != nil {
			messages = append(messages, message)
		}
	}
	prefix := engine.AgentMessages{}
	if cut.IsSplitTurn {
		for index := cut.TurnStartIndex; index < cut.FirstKeptEntryIndex; index++ {
			if message := compactionMessage(entries[index], false); message != nil {
				prefix = append(prefix, message)
			}
		}
	}
	tail := engine.AgentMessages(nil)
	if retainTail {
		tail = make(engine.AgentMessages, 0, len(entries)-cut.FirstKeptEntryIndex)
		for index := cut.FirstKeptEntryIndex; index < len(entries); index++ {
			if message := harnessEntryMessage(entries[index], false); message != nil {
				tail = append(tail, message)
			}
		}
	}
	if len(messages) == 0 && len(prefix) == 0 {
		// Nothing folds: a compaction here would only re-emit the previous summary and
		// duplicate the whole path as its retained tail.
		return nil, nil
	}
	fileOps := newFileOperations()
	if previousIndex >= 0 && (retainTail || !pathEntries[previousIndex].FromHook) {
		mergeDetails(&fileOps, pathEntries[previousIndex].Details)
	}
	for _, message := range messages {
		extractFileOperations(message, &fileOps)
	}
	for _, message := range prefix {
		extractFileOperations(message, &fileOps)
	}
	preparation := &CompactionPreparation{
		MessagesToSummarize: messages,
		TurnPrefixMessages:  prefix,
		RetainedTail:        tail,
		IsSplitTurn:         cut.IsSplitTurn,
		TokensBefore:        EstimateContextTokens(ContextMessages(pathEntries)).Tokens,
		PreviousSummary:     previousSummary,
		FileOps:             fileOps,
		Settings:            settings,
	}
	if !retainTail {
		// The coding-agent variant still records the storage boundary id.
		preparation.FirstKeptEntryID = entries[cut.FirstKeptEntryIndex].ID
	}
	return preparation, nil
}

func ContextMessages(entries []SessionEntry) engine.AgentMessages {
	latest := -1
	for index := range entries {
		if entries[index].Type == "compaction" {
			latest = index
		}
	}
	selected := entries
	if latest >= 0 {
		selected = []SessionEntry{entries[latest]}
		if entries[latest].RetainedTail == nil {
			found := false
			for index := 0; index < latest; index++ {
				if entries[index].ID == entries[latest].FirstKeptEntryID {
					found = true
				}
				if found {
					selected = append(selected, entries[index])
				}
			}
		}
		selected = append(selected, entries[latest+1:]...)
	}
	messages := make(engine.AgentMessages, 0, len(selected))
	for _, entry := range selected {
		if message := entryMessage(entry, true); message != nil {
			messages = append(messages, message)
		}
		if entry.Type == "compaction" {
			messages = append(messages, entry.RetainedTail...)
		}
	}
	return messages
}

//nolint:staticcheck // CompactionError messages match upstream capitalization.
func Compact(
	ctx context.Context,
	preparation *CompactionPreparation,
	model *ai.Model,
	complete CompleteFunc,
	customInstructions string,
	thinkingLevel ai.ModelThinkingLevel,
) (*CompactionResult, error) {
	return compact(ctx, preparation, model, complete, customInstructions, thinkingLevel, nil)
}

// CompactProduct follows the coding-agent summary policy; Compact retains the standalone harness contract.
func CompactProduct(ctx context.Context, preparation *CompactionPreparation, model *ai.Model, complete CompleteFunc, customInstructions string, thinkingLevel ai.ModelThinkingLevel, sessionID *string) (*CompactionResult, error) {
	return compact(ctx, preparation, model, complete, customInstructions, thinkingLevel, &productSummaryOptions{sessionID: sessionID})
}

type productSummaryOptions struct{ sessionID *string }

func compact(ctx context.Context, preparation *CompactionPreparation, model *ai.Model, complete CompleteFunc, customInstructions string, thinkingLevel ai.ModelThinkingLevel, product *productSummaryOptions) (*CompactionResult, error) {
	var summary string
	var summaryUsage ai.Usage
	if preparation.IsSplitTurn && len(preparation.TurnPrefixMessages) > 0 {
		history := "No prior history."
		var historyUsage *ai.Usage
		if len(preparation.MessagesToSummarize) > 0 {
			generated, err := generateSummaryWithUsage(ctx, preparation.MessagesToSummarize, model, complete,
				preparation.Settings.ReserveTokens, customInstructions, preparation.PreviousSummary, thinkingLevel, product)
			if err != nil {
				return nil, err
			}
			history, historyUsage = generated.Text, &generated.Usage
		}
		prefix, err := generateTurnPrefixSummary(ctx, preparation.TurnPrefixMessages, model, complete,
			preparation.Settings.ReserveTokens, thinkingLevel, product)
		if err != nil {
			return nil, err
		}
		summary = history + "\n\n---\n\n**Turn Context (split turn):**\n\n" + prefix.Text
		summaryUsage = prefix.Usage
		if historyUsage != nil {
			summaryUsage = combineUsage(*historyUsage, prefix.Usage)
		}
	} else {
		generated, err := generateSummaryWithUsage(ctx, preparation.MessagesToSummarize, model, complete,
			preparation.Settings.ReserveTokens, customInstructions, preparation.PreviousSummary, thinkingLevel, product)
		if err != nil {
			return nil, err
		}
		summary, summaryUsage = generated.Text, generated.Usage
	}
	readFiles, modifiedFiles := computeFileLists(preparation.FileOps)
	summary += FormatFileOperations(readFiles, modifiedFiles)
	return &CompactionResult{
		Summary: summary, FirstKeptEntryID: preparation.FirstKeptEntryID,
		TokensBefore: preparation.TokensBefore,
		Usage:        &summaryUsage,
		RetainedTail: preparation.RetainedTail,
		Details:      CompactionDetails{ReadFiles: readFiles, ModifiedFiles: modifiedFiles},
	}, nil
}

func GenerateSummary(
	ctx context.Context,
	messages engine.AgentMessages,
	model *ai.Model,
	complete CompleteFunc,
	reserveTokens int64,
	customInstructions string,
	previousSummary *string,
	thinkingLevel ai.ModelThinkingLevel,
) (string, error) {
	result, err := GenerateSummaryWithUsage(ctx, messages, model, complete, reserveTokens, customInstructions, previousSummary, thinkingLevel)
	if err != nil {
		return "", err
	}
	return result.Text, nil
}

type SummaryResult struct {
	Text  string
	Usage ai.Usage
}

func GenerateSummaryWithUsage(
	ctx context.Context,
	messages engine.AgentMessages,
	model *ai.Model,
	complete CompleteFunc,
	reserveTokens int64,
	customInstructions string,
	previousSummary *string,
	thinkingLevel ai.ModelThinkingLevel,
) (*SummaryResult, error) {
	return generateSummaryWithUsage(ctx, messages, model, complete, reserveTokens, customInstructions, previousSummary, thinkingLevel, nil)
}

func generateSummaryWithUsage(ctx context.Context, messages engine.AgentMessages, model *ai.Model, complete CompleteFunc, reserveTokens int64, customInstructions string, previousSummary *string, thinkingLevel ai.ModelThinkingLevel, product *productSummaryOptions) (*SummaryResult, error) {
	base := SummarizationPrompt
	if previousSummary != nil && *previousSummary != "" {
		base = UpdateSummarizationPrompt
	}
	if customInstructions != "" {
		base += "\n\nAdditional focus: " + customInstructions
	}
	prompt := "<conversation>\n" + SerializeConversation(messages) + "\n</conversation>\n\n"
	if previousSummary != nil && *previousSummary != "" {
		prompt += "<previous-summary>\n" + *previousSummary + "\n</previous-summary>\n\n"
	}
	prompt += base
	return runSummary(ctx, prompt, model, complete, minTokenLimit(reserveTokens*8/10, model), thinkingLevel, product)
}

//nolint:staticcheck // CompactionError messages match upstream capitalization.
func generateTurnPrefixSummary(ctx context.Context, messages engine.AgentMessages, model *ai.Model, complete CompleteFunc, reserveTokens int64, thinkingLevel ai.ModelThinkingLevel, product *productSummaryOptions) (*SummaryResult, error) {
	prompt := "<conversation>\n" + SerializeConversation(messages) + "\n</conversation>\n\n" + TurnPrefixSummarizationPrompt
	result, err := runSummary(ctx, prompt, model, complete, minTokenLimit(reserveTokens/2, model), thinkingLevel, product)
	if err != nil {
		if errors.Is(err, context.Canceled) {
			return nil, err
		}
		if err.Error() == "Summarization aborted" {
			return nil, errors.New("Turn prefix summarization aborted")
		}
		if product != nil && err.Error() == "Summarization attempted to call a tool" {
			return nil, errors.New("Turn prefix summarization attempted to call a tool")
		}
		if strings.HasPrefix(err.Error(), "Summarization failed:") {
			return nil, errors.New("Turn prefix summarization failed:" + strings.TrimPrefix(err.Error(), "Summarization failed:"))
		}
		return nil, err
	}
	return result, nil
}

//nolint:staticcheck // CompactionError messages match upstream capitalization.
func runSummary(ctx context.Context, prompt string, model *ai.Model, complete CompleteFunc, maxTokens float64, thinkingLevel ai.ModelThinkingLevel, product *productSummaryOptions) (*SummaryResult, error) {
	if model == nil || complete == nil {
		return nil, errors.New("Summarization failed: no model or completion function")
	}
	system := SummarizationSystemPrompt
	request := ai.Context{
		SystemPrompt: &system,
		Messages:     ai.MessageList{&ai.UserMessage{Content: ai.NewUserContent(&ai.TextContent{Text: prompt})}},
	}
	options := &ai.SimpleStreamOptions{StreamOptions: ai.StreamOptions{MaxTokens: &maxTokens}}
	if model.Reasoning && thinkingLevel != "" && thinkingLevel != ai.ModelThinkingOff {
		level := ai.ThinkingLevel(thinkingLevel)
		options.Reasoning = &level
	}
	response, err := completeSummary(ctx, complete, model, request, options, nil, nil, product)
	if err != nil {
		return nil, fmt.Errorf("Summarization failed: %w", err)
	}
	if product == nil && response.StopReason == ai.StopReasonAborted {
		if response.ErrorMessage != nil && *response.ErrorMessage != "" {
			return nil, errors.New(*response.ErrorMessage)
		}
		return nil, errors.New("Summarization aborted")
	}
	if response.StopReason == ai.StopReasonError {
		message := "Unknown error"
		if response.ErrorMessage != nil && *response.ErrorMessage != "" {
			message = *response.ErrorMessage
		}
		return nil, errors.New("Summarization failed: " + message)
	}
	if product != nil {
		if response.StopReason == ai.StopReasonLength {
			return nil, errors.New("Summarization failed: generation hit the token cap and the summary is incomplete")
		}
		for _, block := range response.Content {
			if _, ok := block.(*ai.ToolCall); ok {
				return nil, errors.New("Summarization attempted to call a tool")
			}
		}
	}
	var texts []string
	for _, block := range response.Content {
		if text, ok := block.(*ai.TextContent); ok {
			texts = append(texts, text.Text)
		}
	}
	return &SummaryResult{Text: strings.Join(texts, "\n"), Usage: response.Usage}, nil
}

func combineUsage(first, second ai.Usage) ai.Usage {
	combined := ai.Usage{
		Input: first.Input + second.Input, Output: first.Output + second.Output,
		CacheRead: first.CacheRead + second.CacheRead, CacheWrite: first.CacheWrite + second.CacheWrite,
		TotalTokens: first.TotalTokens + second.TotalTokens,
		Cost: ai.Cost{
			Input: first.Cost.Input + second.Cost.Input, Output: first.Cost.Output + second.Cost.Output,
			CacheRead: first.Cost.CacheRead + second.Cost.CacheRead, CacheWrite: first.Cost.CacheWrite + second.Cost.CacheWrite,
			Total: first.Cost.Total + second.Cost.Total,
		},
	}
	if first.CacheWrite1h != nil || second.CacheWrite1h != nil {
		value := usagePart(first.CacheWrite1h) + usagePart(second.CacheWrite1h)
		combined.CacheWrite1h = &value
	}
	if first.Reasoning != nil || second.Reasoning != nil {
		value := usagePart(first.Reasoning) + usagePart(second.Reasoning)
		combined.Reasoning = &value
	}
	ai.SetUsageOptionalsBeforeTotals(&combined)
	return combined
}

func usagePart(value *int64) int64 {
	if value == nil {
		return 0
	}
	return *value
}

func SerializeConversation(messages engine.AgentMessages) string {
	parts := make([]string, 0, len(messages))
	for _, original := range messages {
		message := summaryLLMMessage(original)
		switch typed := message.(type) {
		case *ai.UserMessage:
			if text := userText(typed.Content); text != "" {
				parts = append(parts, "[User]: "+text)
			}
		case *ai.AssistantMessage:
			var textParts, thinkingParts, toolCalls []string
			for _, block := range typed.Content {
				switch content := block.(type) {
				case *ai.TextContent:
					textParts = append(textParts, content.Text)
				case *ai.ThinkingContent:
					thinkingParts = append(thinkingParts, content.Thinking)
				case *ai.ToolCall:
					arguments := orderedToolCallArguments(content)
					toolCalls = append(toolCalls, content.Name+"("+strings.Join(arguments, ", ")+")")
				}
			}
			if len(thinkingParts) > 0 {
				parts = append(parts, "[Assistant thinking]: "+strings.Join(thinkingParts, "\n"))
			}
			if len(textParts) > 0 {
				parts = append(parts, "[Assistant]: "+strings.Join(textParts, "\n"))
			}
			if len(toolCalls) > 0 {
				parts = append(parts, "[Assistant tool calls]: "+strings.Join(toolCalls, "; "))
			}
		case *ai.ToolResultMessage:
			if text := toolResultText(typed.Content); text != "" {
				parts = append(parts, "[Tool result]: "+truncateSummary(text, 2000))
			}
		}
	}
	return strings.Join(parts, "\n\n")
}

func FormatFileOperations(readFiles, modifiedFiles []string) string {
	sections := make([]string, 0, 2)
	if len(readFiles) > 0 {
		sections = append(sections, "<read-files>\n"+strings.Join(readFiles, "\n")+"\n</read-files>")
	}
	if len(modifiedFiles) > 0 {
		sections = append(sections, "<modified-files>\n"+strings.Join(modifiedFiles, "\n")+"\n</modified-files>")
	}
	if len(sections) == 0 {
		return ""
	}
	return "\n\n" + strings.Join(sections, "\n\n")
}

func validCutPoints(entries []SessionEntry, startIndex, endIndex int) []int {
	result := make([]int, 0)
	for index := startIndex; index < endIndex; index++ {
		entry := entries[index]
		if entry.Type == "message" {
			switch messageRole(entry.Message) {
			case "bashExecution", "custom", "branchSummary", "compactionSummary", "user", "assistant":
				result = append(result, index)
			}
		}
		if (entry.Type == "branch_summary" && entry.Summary != "") || entry.Type == "custom_message" {
			result = append(result, index)
		}
	}
	return result
}

// The harness-package compaction algorithm (packages/agent/src/harness/
// compaction/compaction.ts) diverges from the coding-agent one mirrored by
// FindCutPoint: only message entries accumulate tokens, the metadata walk-back
// stops at any message entry, only a user-role message cut avoids the
// split-turn classification, and branch_summary entries count regardless of
// an empty summary.
// harnessValidCutPoints lists the entries a compaction may keep from. Boundaries that fall
// between an assistant tool call and its results are skipped: cutting there would retain a
// tool result whose call was summarized away, which providers reject as an orphan output.
func harnessValidCutPoints(entries []SessionEntry, startIndex, endIndex int) []int {
	result := make([]int, 0)
	pendingResults := 0
	for index := startIndex; index < endIndex; index++ {
		entry := entries[index]
		if entry.Type == "message" {
			message := harnessEntryMessage(entry, false)
			switch messageRole(message) {
			case "assistant":
				pendingResults = harnessToolCallCount(message)
				result = append(result, index)
			case "toolResult":
				if pendingResults > 0 {
					pendingResults--
				}
			case "user":
				pendingResults = 0
				result = append(result, index)
			case "bashExecution", "custom", "branchSummary", "compactionSummary":
				if pendingResults == 0 {
					result = append(result, index)
				}
			}
			continue
		}
		if (entry.Type == "branch_summary" || entry.Type == "custom_message") && pendingResults == 0 {
			result = append(result, index)
		}
	}
	return result
}

func harnessToolCallCount(message engine.AgentMessage) int {
	var content ai.AssistantContent
	switch typed := message.(type) {
	case *ai.AssistantMessage:
		content = typed.Content
	case ai.AssistantMessage:
		content = typed.Content
	}
	count := 0
	for _, block := range content {
		if _, ok := block.(*ai.ToolCall); ok {
			count++
		}
	}
	return count
}

func harnessFindTurnStartIndex(entries []SessionEntry, entryIndex, startIndex int) int {
	for index := entryIndex; index >= startIndex; index-- {
		entry := entries[index]
		if entry.Type == "branch_summary" || entry.Type == "custom_message" {
			return index
		}
		if entry.Type == "message" {
			switch messageRole(harnessEntryMessage(entry, false)) {
			case "user", "bashExecution":
				return index
			}
		}
	}
	return -1
}

func harnessFindCutPoint(entries []SessionEntry, startIndex, endIndex int, keepRecentTokens int64) CutPointResult {
	cutPoints := harnessValidCutPoints(entries, startIndex, endIndex)
	if len(cutPoints) == 0 {
		return CutPointResult{FirstKeptEntryIndex: startIndex, TurnStartIndex: -1}
	}
	var accumulated int64
	cutIndex := cutPoints[0]
	for index := endIndex - 1; index >= startIndex; index-- {
		if entries[index].Type != "message" {
			continue
		}
		accumulated += EstimateTokens(harnessEntryMessage(entries[index], false))
		if accumulated >= keepRecentTokens {
			chosen := -1
			for _, candidate := range cutPoints {
				if candidate >= index {
					chosen = candidate
					break
				}
			}
			if chosen < 0 {
				// The newest messages alone exceed the recent budget (typically one large
				// tool result). Cut at the latest boundary before them so the turn still
				// folds instead of retaining the whole path and compacting nothing.
				for _, candidate := range cutPoints {
					if candidate > index {
						break
					}
					chosen = candidate
				}
			}
			if chosen >= 0 {
				cutIndex = chosen
			}
			break
		}
	}
	for cutIndex > startIndex {
		previous := entries[cutIndex-1]
		if previous.Type == "compaction" || previous.Type == "message" {
			break
		}
		cutIndex--
	}
	cutEntry := entries[cutIndex]
	isUserMessage := cutEntry.Type == "message" && messageRole(harnessEntryMessage(cutEntry, false)) == "user"
	turnStart := -1
	if !isUserMessage {
		turnStart = harnessFindTurnStartIndex(entries, cutIndex, startIndex)
	}
	return CutPointResult{
		FirstKeptEntryIndex: cutIndex,
		TurnStartIndex:      turnStart,
		IsSplitTurn:         !isUserMessage && turnStart != -1,
	}
}

func assistantUsage(message engine.AgentMessage) *ai.Usage {
	var assistant *ai.AssistantMessage
	switch typed := message.(type) {
	case *ai.AssistantMessage:
		assistant = typed
	case ai.AssistantMessage:
		assistant = &typed
	default:
		return nil
	}
	if assistant.StopReason == ai.StopReasonAborted || assistant.StopReason == ai.StopReasonError || CalculateContextTokens(assistant.Usage) <= 0 {
		return nil
	}
	return &assistant.Usage
}

func entryMessage(entry SessionEntry, includeCompaction bool) engine.AgentMessage {
	// Mirrors coding-agent sessionEntryToContextMessages: an empty-summary
	// branch_summary is invisible metadata (session-manager.ts guards with
	// `entry.type === "branch_summary" && entry.summary`).
	if entry.Type == "branch_summary" && entry.Summary == "" {
		return nil
	}
	return harnessEntryMessage(entry, includeCompaction)
}

// harnessEntryMessage mirrors harness getMessageFromEntry, which projects a
// branch_summary even when its summary is empty.
func harnessEntryMessage(entry SessionEntry, includeCompaction bool) engine.AgentMessage {
	switch entry.Type {
	case "message":
		if raw, ok := entry.Message.(json.RawMessage); ok {
			if message, err := ai.UnmarshalMessage(raw); err == nil {
				return message
			}
		}
		return entry.Message
	case "custom_message":
		return &CustomMessage{Role: "custom", CustomType: entry.CustomType, Content: entry.Content, Display: entry.Display, Details: entry.Details, Timestamp: parseTimestamp(entry.Timestamp)}
	case "branch_summary":
		return &SummaryMessage{Role: "branchSummary", Summary: entry.Summary, FromID: entry.FromID, Timestamp: parseTimestamp(entry.Timestamp)}
	case "compaction":
		if includeCompaction {
			return &SummaryMessage{Role: "compactionSummary", Summary: entry.Summary, TokensBefore: entry.TokensBefore, Timestamp: parseTimestamp(entry.Timestamp)}
		}
	}
	return nil
}

func messageRole(message engine.AgentMessage) string {
	switch typed := message.(type) {
	case *ai.UserMessage, ai.UserMessage:
		return "user"
	case *ai.AssistantMessage, ai.AssistantMessage:
		return "assistant"
	case *ai.ToolResultMessage, ai.ToolResultMessage:
		return "toolResult"
	case CustomMessage, *CustomMessage:
		return "custom"
	case BashExecutionMessage, *BashExecutionMessage:
		return "bashExecution"
	case SummaryMessage:
		return typed.Role
	case *SummaryMessage:
		return typed.Role
	}
	encoded, err := ai.Marshal(message)
	if err != nil {
		return ""
	}
	var envelope struct {
		Role string `json:"role"`
	}
	_ = json.Unmarshal(encoded, &envelope)
	return envelope.Role
}

func userContentChars(content ai.UserContent) int64 {
	if content.Text != nil {
		return jsLength(*content.Text)
	}
	var chars int64
	for _, block := range content.Blocks {
		switch typed := block.(type) {
		case *ai.TextContent:
			chars += jsLength(typed.Text)
		case *ai.ImageContent:
			chars += estimatedImageChars
		}
	}
	return chars
}

func assistantContentChars(content ai.AssistantContent) int64 {
	var chars int64
	for _, block := range content {
		switch typed := block.(type) {
		case *ai.TextContent:
			chars += jsLength(typed.Text)
		case *ai.ThinkingContent:
			chars += jsLength(typed.Thinking)
		case *ai.ToolCall:
			chars += jsLength(typed.Name)
			encoded, err := ai.Marshal(typed.Arguments)
			if err != nil {
				chars += jsLength("[unserializable]")
			} else {
				chars += jsLength(string(encoded))
			}
		}
	}
	return chars
}

func toolResultChars(content ai.ToolResultContent) int64 {
	var chars int64
	for _, block := range content {
		switch typed := block.(type) {
		case *ai.TextContent:
			chars += jsLength(typed.Text)
		case *ai.ImageContent:
			chars += estimatedImageChars
		}
	}
	return chars
}

func contentChars(content any) int64 {
	switch typed := content.(type) {
	case string:
		return jsLength(typed)
	case ai.UserContent:
		return userContentChars(typed)
	case ai.UserContentBlocks:
		return userContentChars(ai.NewUserContent(typed...))
	default:
		encoded, err := ai.Marshal(content)
		if err != nil {
			return 0
		}
		var blocks []struct{ Type, Text string }
		if json.Unmarshal(encoded, &blocks) == nil {
			var chars int64
			for _, block := range blocks {
				switch block.Type {
				case "text":
					chars += jsLength(block.Text)
				case "image":
					chars += estimatedImageChars
				}
			}
			return chars
		}
	}
	return 0
}

func estimateRawMessage(raw []byte) int64 {
	var envelope struct {
		Role    string          `json:"role"`
		Content json.RawMessage `json:"content"`
		Summary string          `json:"summary"`
		Command string          `json:"command"`
		Output  string          `json:"output"`
	}
	if json.Unmarshal(raw, &envelope) != nil {
		return 0
	}
	switch envelope.Role {
	case "user", "custom", "toolResult":
		var text string
		if json.Unmarshal(envelope.Content, &text) == nil {
			return ceilQuarter(jsLength(text))
		}
		var blocks []struct{ Type, Text string }
		_ = json.Unmarshal(envelope.Content, &blocks)
		var chars int64
		for _, block := range blocks {
			switch block.Type {
			case "text":
				chars += jsLength(block.Text)
			case "image":
				chars += estimatedImageChars
			}
		}
		return ceilQuarter(chars)
	case "bashExecution":
		return ceilQuarter(jsLength(envelope.Command) + jsLength(envelope.Output))
	case "branchSummary", "compactionSummary":
		return ceilQuarter(jsLength(envelope.Summary))
	case "assistant":
		var assistant ai.AssistantMessage
		if json.Unmarshal(raw, &assistant) == nil {
			return ceilQuarter(assistantContentChars(assistant.Content))
		}
	}
	return 0
}

func extractFileOperations(message engine.AgentMessage, operations *FileOperations) {
	var content ai.AssistantContent
	switch typed := message.(type) {
	case *ai.AssistantMessage:
		content = typed.Content
	case ai.AssistantMessage:
		content = typed.Content
	default:
		return
	}
	for _, block := range content {
		call, ok := block.(*ai.ToolCall)
		if !ok {
			continue
		}
		path, ok := call.Arguments["path"].(string)
		if !ok || path == "" {
			continue
		}
		switch call.Name {
		case "read":
			operations.Read[path] = struct{}{}
		case "write":
			operations.Written[path] = struct{}{}
		case "edit":
			operations.Edited[path] = struct{}{}
		}
	}
}

func newFileOperations() FileOperations {
	return FileOperations{Read: map[string]struct{}{}, Written: map[string]struct{}{}, Edited: map[string]struct{}{}}
}

func mergeDetails(operations *FileOperations, details any) {
	encoded, err := ai.Marshal(details)
	if err != nil {
		return
	}
	var decoded CompactionDetails
	if json.Unmarshal(encoded, &decoded) != nil {
		return
	}
	for _, path := range decoded.ReadFiles {
		operations.Read[path] = struct{}{}
	}
	for _, path := range decoded.ModifiedFiles {
		operations.Edited[path] = struct{}{}
	}
}

func computeFileLists(operations FileOperations) ([]string, []string) {
	modifiedSet := make(map[string]struct{}, len(operations.Edited)+len(operations.Written))
	for path := range operations.Edited {
		modifiedSet[path] = struct{}{}
	}
	for path := range operations.Written {
		modifiedSet[path] = struct{}{}
	}
	read := make([]string, 0, len(operations.Read))
	for path := range operations.Read {
		if _, modified := modifiedSet[path]; !modified {
			read = append(read, path)
		}
	}
	modified := make([]string, 0, len(modifiedSet))
	for path := range modifiedSet {
		modified = append(modified, path)
	}
	sort.Strings(read)
	sort.Strings(modified)
	return read, modified
}

func minTokenLimit(limit int64, model *ai.Model) float64 {
	if model != nil && model.MaxTokens > 0 && model.MaxTokens < float64(limit) {
		return model.MaxTokens
	}
	return float64(limit)
}

func ceilQuarter(chars int64) int64 { return (chars + 3) / 4 }
func jsLength(value string) int64   { return int64(len(utf16.Encode([]rune(value)))) }

func userText(content ai.UserContent) string {
	if content.Text != nil {
		return *content.Text
	}
	var text strings.Builder
	for _, block := range content.Blocks {
		if typed, ok := block.(*ai.TextContent); ok {
			text.WriteString(typed.Text)
		}
	}
	return text.String()
}
func toolResultText(content ai.ToolResultContent) string {
	var text strings.Builder
	for _, block := range content {
		if typed, ok := block.(*ai.TextContent); ok {
			text.WriteString(typed.Text)
		}
	}
	return text.String()
}

func truncateSummary(text string, maxChars int) string {
	units := utf16.Encode([]rune(text))
	if len(units) <= maxChars {
		return text
	}
	return string(utf16.Decode(units[:maxChars])) + fmt.Sprintf("\n\n[... %d more characters truncated]", len(units)-maxChars)
}

func parseTimestamp(value string) int64 {
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return 0
	}
	return parsed.UnixMilli()
}

func summaryLLMMessage(message engine.AgentMessage) ai.Message {
	switch typed := message.(type) {
	case *ai.UserMessage:
		return typed
	case ai.UserMessage:
		copy := typed
		return &copy
	case *ai.AssistantMessage:
		return typed
	case ai.AssistantMessage:
		copy := typed
		return &copy
	case *ai.ToolResultMessage:
		return typed
	case ai.ToolResultMessage:
		copy := typed
		return &copy
	case CustomMessage:
		return customSummaryMessage(typed.Content, typed.Timestamp)
	case *CustomMessage:
		if typed == nil {
			return nil
		}
		return customSummaryMessage(typed.Content, typed.Timestamp)
	case BashExecutionMessage:
		return bashSummaryMessage(&typed)
	case *BashExecutionMessage:
		return bashSummaryMessage(typed)
	case SummaryMessage:
		return wrappedSummaryMessage(&typed)
	case *SummaryMessage:
		return wrappedSummaryMessage(typed)
	case json.RawMessage:
		return summaryMessageFromJSON(typed)
	case []byte:
		return summaryMessageFromJSON(typed)
	default:
		encoded, err := ai.Marshal(message)
		if err != nil {
			return nil
		}
		return summaryMessageFromJSON(encoded)
	}
}

func summaryMessageFromJSON(encoded []byte) ai.Message {
	if message, err := ai.UnmarshalMessage(encoded); err == nil {
		return message
	}
	var envelope struct {
		Role string `json:"role"`
	}
	if json.Unmarshal(encoded, &envelope) != nil {
		return nil
	}
	switch envelope.Role {
	case "custom":
		var message CustomMessage
		if json.Unmarshal(encoded, &message) == nil {
			return customSummaryMessage(message.Content, message.Timestamp)
		}
	case "bashExecution":
		var message BashExecutionMessage
		if json.Unmarshal(encoded, &message) == nil {
			return bashSummaryMessage(&message)
		}
	case "branchSummary", "compactionSummary":
		var message SummaryMessage
		if json.Unmarshal(encoded, &message) == nil {
			return wrappedSummaryMessage(&message)
		}
	}
	return nil
}

// Returns ai.Message, not *ai.UserMessage: a typed nil would survive the
// interface conversion and panic in SerializeConversation's type switch.
func customSummaryMessage(content any, timestamp int64) ai.Message {
	var userContent ai.UserContent
	switch typed := content.(type) {
	case string:
		userContent = ai.NewUserContent(&ai.TextContent{Text: typed})
	case ai.UserContent:
		userContent = typed
	case ai.UserContentBlocks:
		userContent = ai.NewUserContent(typed...)
	default:
		encoded, err := ai.Marshal(content)
		if err != nil || json.Unmarshal(encoded, &userContent) != nil {
			return nil
		}
	}
	return &ai.UserMessage{Content: userContent, Timestamp: timestamp}
}

func bashSummaryMessage(message *BashExecutionMessage) ai.Message {
	if message == nil || message.ExcludeFromContext != nil && *message.ExcludeFromContext {
		return nil
	}
	text := "Ran `" + message.Command + "`\n"
	if message.Output != "" {
		text += "```\n" + message.Output + "\n```"
	} else {
		text += "(no output)"
	}
	if message.Cancelled {
		text += "\n\n(command cancelled)"
	} else if message.ExitCode != nil && *message.ExitCode != 0 {
		text += fmt.Sprintf("\n\nCommand exited with code %d", *message.ExitCode)
	}
	if message.Truncated && message.FullOutputPath != nil {
		text += "\n\n[Output truncated. Full output: " + *message.FullOutputPath + "]"
	}
	return &ai.UserMessage{Content: ai.NewUserContent(&ai.TextContent{Text: text}), Timestamp: message.Timestamp}
}

func wrappedSummaryMessage(message *SummaryMessage) ai.Message {
	if message == nil {
		return nil
	}
	var text string
	switch message.Role {
	case "branchSummary":
		text = branchSummaryPrefix + message.Summary + branchSummarySuffix
	case "compactionSummary":
		text = compactionSummaryPrefix + message.Summary + compactionSummarySuffix
	default:
		return nil
	}
	return &ai.UserMessage{Content: ai.NewUserContent(&ai.TextContent{Text: text}), Timestamp: message.Timestamp}
}

func orderedToolCallArguments(call *ai.ToolCall) []string {
	encoded, err := ai.MarshalToolCallArguments(call)
	if err == nil {
		decoder := json.NewDecoder(bytes.NewReader(encoded))
		if token, tokenErr := decoder.Token(); tokenErr == nil && token == json.Delim('{') {
			arguments := make([]string, 0, len(call.Arguments))
			for decoder.More() {
				key, keyErr := decoder.Token()
				if keyErr != nil {
					arguments = nil
					break
				}
				var value json.RawMessage
				if valueErr := decoder.Decode(&value); valueErr != nil {
					arguments = nil
					break
				}
				arguments = append(arguments, key.(string)+"="+string(value))
			}
			if arguments != nil {
				return arguments
			}
		}
	}
	keys := make([]string, 0, len(call.Arguments))
	for key := range call.Arguments {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	arguments := make([]string, 0, len(keys))
	for _, key := range keys {
		value, valueErr := ai.Marshal(call.Arguments[key])
		if valueErr != nil {
			value = []byte("[unserializable]")
		}
		arguments = append(arguments, key+"="+string(value))
	}
	return arguments
}

func completeSummary(ctx context.Context, complete CompleteFunc, model *ai.Model, request ai.Context, options *ai.SimpleStreamOptions, retry *ai.RetryPolicy, callbacks *ai.RetryCallbacks, product *productSummaryOptions) (*ai.AssistantMessage, error) {
	if product == nil {
		return CompleteSimpleWithRetries(ctx, complete, model, request, options, retry, callbacks)
	}
	copied := *options
	retention := ai.CacheRetentionNone
	copied.CacheRetention = &retention
	copied.SessionID = product.sessionID
	if copied.SessionID == nil {
		id, err := ai.UUIDv7()
		if err != nil {
			return nil, err
		}
		copied.SessionID = &id
	}
	return ai.RetryAssistantCall(ctx, func() (*ai.AssistantMessage, error) { return complete(ctx, model, request, &copied) }, retry, callbacks)
}
