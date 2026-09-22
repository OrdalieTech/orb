//go:build orb_nodefaultproviders

package all

import "github.com/OrdalieTech/orb/ai/api"

// Registry returns an empty registry: this build links no default provider,
// so every stream function must be supplied explicitly.
func Registry() *api.Registry { return api.NewRegistry() }
