package modes

import (
	"context"

	aiauth "github.com/OrdalieTech/pi-go/ai/auth"
	"github.com/OrdalieTech/pi-go/codingagent"
	"github.com/OrdalieTech/pi-go/codingagent/config"
	"github.com/OrdalieTech/pi-go/codingagent/extensions"
	sessionstore "github.com/OrdalieTech/pi-go/codingagent/session"
)

// InteractiveSessionHost owns the live SessionRuntime behind the interactive
// TUI and executes every state-changing session command. It mirrors upstream
// AgentSessionRuntime (core/agent-session-runtime.ts): replacement methods
// shut down and dispose the current runtime before calling the full CLI creation
// path, apply and rebind the replacement without rollback, and only then let
// the replacement's session_start extension event fire.
//
// UI selection stays with the TUI: /clone is Fork of the current leaf with
// position "at", and /tree navigation goes through the extension command
// context's NavigateTree, which the runtime serves itself.
type InteractiveSessionHost interface {
	Session() *codingagent.SessionRuntime

	// SetRebindSession registers the TUI callback invoked after the replacement
	// becomes current and before its session_start fires.
	SetRebindSession(func(*codingagent.SessionRuntime) error)
	// SetBeforeSessionInvalidate registers synchronous listener teardown run
	// after session_shutdown and before the current runtime is disposed.
	SetBeforeSessionInvalidate(func())

	NewSession(ctx context.Context, options *extensions.NewSessionOptions) (extensions.SessionReplacementResult, error)
	SwitchSession(ctx context.Context, sessionPath, cwdOverride string, options *extensions.SwitchSessionOptions) (extensions.SessionReplacementResult, error)
	Fork(ctx context.Context, entryID string, options *extensions.ForkOptions) (InteractiveForkResult, error)
	ImportSession(ctx context.Context, inputPath, cwdOverride string) (extensions.SessionReplacementResult, error)
	Reload(ctx context.Context) error

	// ListProjectSessions lists sessions for the current cwd;
	// ListAllSessions lists every project's sessions (upstream
	// SessionManager.list / listAll semantics).
	ListProjectSessions(onProgress sessionstore.SessionListProgress) []sessionstore.SessionInfo
	ListAllSessions(onProgress sessionstore.SessionListProgress) []sessionstore.SessionInfo

	TrustState() (InteractiveTrustState, error)
	// SetProjectTrust persists the trust decisions and rebuilds the runtime
	// through the reload path so resources and tools reflect the new trust.
	SetProjectTrust(ctx context.Context, updates []config.ProjectTrustUpdate) error

	AuthOptions(ctx context.Context) (InteractiveAuthOptions, error)
	Login(ctx context.Context, providerID string, authType aiauth.AuthType, interaction aiauth.AuthInteraction) error
	Logout(ctx context.Context, providerID string) error

	Dispose()
}

type InteractiveForkResult struct {
	Cancelled    bool
	SelectedText string
}

type InteractiveTrustState struct {
	CWD            string
	ProjectTrusted bool
	SavedDecision  *config.ProjectTrustStoreEntry
	Options        []config.ProjectTrustOption
}

type InteractiveAuthProvider struct {
	ID             string
	Name           string
	AuthType       aiauth.AuthType
	MethodName     string
	LoginLabel     string
	Configured     bool
	Status         *InteractiveAuthStatus
	LoginAvailable bool
}

type InteractiveAuthStatus struct {
	Type   aiauth.AuthType
	Source string
}

type InteractiveAuthOptions struct {
	Login  []InteractiveAuthProvider
	Logout []InteractiveAuthProvider
}

// MissingSessionCwdError reports a session whose stored cwd no longer exists;
// the TUI prompts for a cwd override and retries. Text matches upstream
// formatMissingSessionCwdError.
type MissingSessionCwdError struct {
	SessionFile string
	SessionCWD  string
	FallbackCWD string
}

func (err *MissingSessionCwdError) Error() string {
	sessionFile := ""
	if err.SessionFile != "" {
		sessionFile = "\nSession file: " + err.SessionFile
	}
	return "Stored session working directory does not exist: " + err.SessionCWD + sessionFile + "\nCurrent working directory: " + err.FallbackCWD
}

func formatMissingSessionCwdPrompt(err *MissingSessionCwdError) string {
	return "cwd from session file does not exist\n" + err.SessionCWD + "\n\ncontinue in current cwd\n" + err.FallbackCWD
}

// SessionImportFileNotFoundError reports an /import path that does not exist.
// Text matches upstream SessionImportFileNotFoundError.
type SessionImportFileNotFoundError struct {
	FilePath string
}

func (err *SessionImportFileNotFoundError) Error() string {
	return "File not found: " + err.FilePath
}
