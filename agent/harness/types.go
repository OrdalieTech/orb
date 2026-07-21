package harness

import (
	"context"
	"fmt"

	"github.com/OrdalieTech/pigo/agent"
	"github.com/OrdalieTech/pigo/ai"
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
	FileErrorAborted          FileErrorCode = "aborted"
	FileErrorNotFound         FileErrorCode = "not_found"
	FileErrorPermissionDenied FileErrorCode = "permission_denied"
	FileErrorNotDirectory     FileErrorCode = "not_directory"
	FileErrorIsDirectory      FileErrorCode = "is_directory"
	FileErrorInvalid          FileErrorCode = "invalid"
	FileErrorNotSupported     FileErrorCode = "not_supported"
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
	MTimeMS float64
}

// ResourceFileSystem is the read-only filesystem slice used by resource discovery.
type ResourceFileSystem interface {
	ResourceFileInfo(path string) (FileInfo, error)
	ResourceListDir(path string) ([]FileInfo, error)
	ResourceReadTextFile(path string) (string, error)
	ResourceCanonicalPath(path string) (string, error)
}

type ExecutionErrorCode string

const (
	ExecutionErrorAborted          ExecutionErrorCode = "aborted"
	ExecutionErrorTimeout          ExecutionErrorCode = "timeout"
	ExecutionErrorShellUnavailable ExecutionErrorCode = "shell_unavailable"
	ExecutionErrorSpawn            ExecutionErrorCode = "spawn_error"
	ExecutionErrorCallback         ExecutionErrorCode = "callback_error"
	ExecutionErrorUnknown          ExecutionErrorCode = "unknown"
)

// ExecutionError keeps expected shell failures stable across execution backends.
type ExecutionError struct {
	Code ExecutionErrorCode
	Err  error
}

func (err *ExecutionError) Error() string {
	if err == nil {
		return ""
	}
	if err.Err != nil {
		return err.Err.Error()
	}
	return string(err.Code)
}

func (err *ExecutionError) Unwrap() error {
	if err == nil {
		return nil
	}
	return err.Err
}

// FileSystem is the harness filesystem seam. Context cancellation maps to FileErrorAborted.
type FileSystem interface {
	WorkingDirectory() string
	AbsolutePath(context.Context, string) (string, error)
	JoinPath(context.Context, ...string) (string, error)
	ReadTextFile(context.Context, string) (string, error)
	ReadTextLines(context.Context, string, int) ([]string, error)
	ReadBinaryFile(context.Context, string) ([]byte, error)
	WriteFile(context.Context, string, []byte) error
	AppendFile(context.Context, string, []byte) error
	FileInfo(context.Context, string) (FileInfo, error)
	ListDir(context.Context, string) ([]FileInfo, error)
	CanonicalPath(context.Context, string) (string, error)
	Exists(context.Context, string) (bool, error)
	CreateDir(context.Context, string, bool) error
	Remove(context.Context, string, bool, bool) error
	CreateTempDir(context.Context, string) (string, error)
	CreateTempFile(context.Context, string, string) (string, error)
	Cleanup() error
}

type ExecOptions struct {
	CWD            string
	Env            map[string]string
	TimeoutSeconds *float64
	OnStdout       func(string) error
	OnStderr       func(string) error
}

type ExecResult struct {
	Stdout   string `json:"stdout"`
	Stderr   string `json:"stderr"`
	ExitCode int    `json:"exitCode"`
}

type Shell interface {
	Exec(context.Context, string, ExecOptions) (ExecResult, error)
	Cleanup() error
}

type ExecutionEnv interface {
	FileSystem
	Shell
}

// SessionEntry is the compaction-facing projection of a v3 session entry.
// Harness storage owns generic session persistence; codingagent/session remains an upper-layer wire manager.
type SessionEntry struct {
	Type             string
	ID               string
	ParentID         *string
	Timestamp        string
	Message          agent.AgentMessage
	Summary          string
	FirstKeptEntryID string
	TokensBefore     float64
	Details          any
	Usage            *ai.Usage
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
	Role         string  `json:"role"`
	Summary      string  `json:"summary"`
	FromID       string  `json:"fromId,omitempty"`
	TokensBefore float64 `json:"tokensBefore,omitempty"`
	Timestamp    int64   `json:"timestamp"`
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
	Usage                *ai.Usage         `json:"usage,omitempty"`
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
	Usage         *ai.Usage
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
