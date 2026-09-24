// Package host bundles the ports through which the portable core reaches a
// platform (DECISIONS.md P10). A platform supplies a Host; the core derives its
// settings, credentials, model catalogs and session journals from it
// instead of the process environment, home directory or disk.
package host

import (
	"context"
	"path"
	"path/filepath"

	aiauth "github.com/OrdalieTech/orb/ai/auth"
	"github.com/OrdalieTech/orb/engine/harness"
)

// Document updates must commit before returning; nil deletes the document.
type Document interface {
	Read(context.Context) ([]byte, error)
	Update(context.Context, func([]byte) ([]byte, error)) error
}

// Store is the durable-document port. Paths are the kernel file locations
// (for example AgentDir/settings.json); backends may map them to files, rows
// or object keys.
type Store interface {
	Document(path string) Document
}

type Host struct {
	// AgentDir is the global configuration root in the host's path namespace.
	AgentDir string
	// FS is the file port for tools, resources and session journals.
	FS harness.FileSystem
	// Exec runs processes; nil on platforms that cannot, which omits process tools.
	Exec harness.Shell
	// Store holds settings, credentials and model catalogs.
	Store Store
	// Env supplies ambient provider credentials and file probes; nil uses the
	// process environment.
	Env aiauth.AuthContext
	// Sessions persists session journals; nil keeps JSONL journals on FS under
	// AgentDir/sessions, the upstream harness layout.
	Sessions harness.SessionRepo
}

// Document returns the named kernel document under AgentDir, keyed like the
// native store so one backend can serve both.
func (h *Host) Document(name string) Document {
	return h.Store.Document(filepath.Join(h.AgentDir, name))
}

// SessionRepo returns Sessions or the FS-backed JSONL repository.
func (h *Host) SessionRepo() harness.SessionRepo {
	if h.Sessions != nil {
		return h.Sessions
	}
	return harness.NewJSONLSessionRepo(h.FS, path.Join(h.AgentDir, "sessions"))
}
