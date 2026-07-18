package harness

import (
	"context"
	"fmt"

	"github.com/OrdalieTech/pi-go/agent"
	"github.com/OrdalieTech/pi-go/ai"
)

// Skill is the harness-level, execution-environment-neutral Agent Skills shape.
type Skill struct {
	Name                   string
	Description            string
	Content                string
	FilePath               string
	DisableModelInvocation bool
}

// PromptTemplate is the harness-level explicit prompt expansion resource.
type PromptTemplate struct {
	Name        string
	Description string
	Content     string
}

type FileKind string

const (
	FileKindFile      FileKind = "file"
	FileKindDirectory FileKind = "directory"
	FileKindSymlink   FileKind = "symlink"
)

type FileErrorCode string

const (
	FileErrorNotFound         FileErrorCode = "not_found"
	FileErrorPermissionDenied FileErrorCode = "permission_denied"
	FileErrorNotDirectory     FileErrorCode = "not_directory"
	FileErrorInvalid          FileErrorCode = "invalid"
	FileErrorUnknown          FileErrorCode = "unknown"
)

// FileError keeps expected filesystem failures typed across local and remote environments.
type FileError struct {
	Code FileErrorCode
	Path string
	Err  error
}

func (err *FileError) Error() string {
	if err == nil {
		return ""
	}
	if err.Err != nil {
		return err.Err.Error()
	}
	return fmt.Sprintf("%s: %s", err.Code, err.Path)
}

func (err *FileError) Unwrap() error {
	if err == nil {
		return nil
	}
	return err.Err
}

type FileInfo struct {
	Name    string
	Path    string
	Kind    FileKind
	Size    int64
	MTimeMS int64
}

// ExecutionEnv is the filesystem slice of the harness environment needed by skill discovery.
// Later harness operations can extend the environment without coupling skill loading to os.File.
type ExecutionEnv interface {
	FileInfo(path string) (FileInfo, error)
	ListDir(path string) ([]FileInfo, error)
	ReadTextFile(path string) (string, error)
	CanonicalPath(path string) (string, error)
}

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
	Timestamp          int64   `json:"timestamp"`
	ExcludeFromContext *bool   `json:"excludeFromContext,omitempty"`
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
