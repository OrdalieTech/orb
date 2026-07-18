package harness

import (
	"context"

	"github.com/OrdalieTech/pi-go/agent"
	"github.com/OrdalieTech/pi-go/ai"
)

// SessionEntry is the compaction-facing projection of a v3 session entry.
// Persistence stays owned by codingagent/session.
type SessionEntry struct {
	Type             string
	ID               string
	ParentID         *string
	Timestamp        string
	Message          agent.AgentMessage
	Summary          string
	FirstKeptEntryID string
	TokensBefore     int64
	Details          any
	FromHook         bool
	FromID           string
	CustomType       string
	Content          any
	Display          bool
}

type CustomMessage struct {
	Role       string `json:"role"`
	CustomType string `json:"customType"`
	Content    any    `json:"content"`
	Display    bool   `json:"display"`
	Details    any    `json:"details,omitempty"`
	Timestamp  int64  `json:"timestamp"`
}

type BashExecutionMessage struct {
	Role               string  `json:"role"`
	Command            string  `json:"command"`
	Output             string  `json:"output"`
	ExitCode           *int    `json:"exitCode"`
	Cancelled          bool    `json:"cancelled"`
	Truncated          bool    `json:"truncated"`
	FullOutputPath     *string `json:"fullOutputPath,omitempty"`
	ExcludeFromContext bool    `json:"excludeFromContext,omitempty"`
	Timestamp          int64   `json:"timestamp"`
}

type SummaryMessage struct {
	Role         string `json:"role"`
	Summary      string `json:"summary"`
	FromID       string `json:"fromId,omitempty"`
	TokensBefore int64  `json:"tokensBefore,omitempty"`
	Timestamp    int64  `json:"timestamp"`
}

type CompactionSettings struct {
	Enabled          bool  `json:"enabled"`
	ReserveTokens    int64 `json:"reserveTokens"`
	KeepRecentTokens int64 `json:"keepRecentTokens"`
}

var DefaultCompactionSettings = CompactionSettings{
	Enabled:          true,
	ReserveTokens:    16384,
	KeepRecentTokens: 20000,
}

type FileOperations struct {
	Read    map[string]struct{}
	Written map[string]struct{}
	Edited  map[string]struct{}
}

type CompactionPreparation struct {
	FirstKeptEntryID    string
	MessagesToSummarize agent.AgentMessages
	TurnPrefixMessages  agent.AgentMessages
	IsSplitTurn         bool
	TokensBefore        int64
	PreviousSummary     *string
	FileOps             FileOperations
	Settings            CompactionSettings
}

type CompactionDetails struct {
	ReadFiles     []string `json:"readFiles"`
	ModifiedFiles []string `json:"modifiedFiles"`
}

type CompactionResult struct {
	Summary              string            `json:"summary"`
	FirstKeptEntryID     string            `json:"firstKeptEntryId"`
	TokensBefore         int64             `json:"tokensBefore"`
	EstimatedTokensAfter int64             `json:"estimatedTokensAfter"`
	Details              CompactionDetails `json:"details"`
}

type ContextUsageEstimate struct {
	Tokens         int64 `json:"tokens"`
	UsageTokens    int64 `json:"usageTokens"`
	TrailingTokens int64 `json:"trailingTokens"`
	LastUsageIndex *int  `json:"lastUsageIndex"`
}

type ContextUsage struct {
	Tokens        *int64   `json:"tokens"`
	ContextWindow float64  `json:"contextWindow"`
	Percent       *float64 `json:"percent"`
}

type CutPointResult struct {
	FirstKeptEntryIndex int  `json:"firstKeptEntryIndex"`
	TurnStartIndex      int  `json:"turnStartIndex"`
	IsSplitTurn         bool `json:"isSplitTurn"`
}

type CompleteFunc func(
	context.Context,
	*ai.Model,
	ai.Context,
	*ai.SimpleStreamOptions,
) (*ai.AssistantMessage, error)

type BranchPreparation struct {
	Messages    agent.AgentMessages
	FileOps     FileOperations
	TotalTokens int64
}

type CollectEntriesResult struct {
	Entries          []SessionEntry
	CommonAncestorID *string
}

type BranchSummaryDetails struct {
	ReadFiles     []string `json:"readFiles"`
	ModifiedFiles []string `json:"modifiedFiles"`
}

type BranchSummaryResult struct {
	Summary       string
	ReadFiles     []string
	ModifiedFiles []string
}

type GenerateBranchSummaryOptions struct {
	Model               *ai.Model
	Complete            CompleteFunc
	CustomInstructions  string
	ReplaceInstructions bool
	ReserveTokens       *int64
}
