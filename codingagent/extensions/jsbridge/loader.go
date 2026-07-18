package jsbridge

import (
	"context"
	"errors"
	"strings"
	"sync"

	"github.com/OrdalieTech/pi-go/codingagent/extensions"
)

type LoadError struct {
	Path  string `json:"path"`
	Error string `json:"error"`
}

type LoadResult struct {
	Registry *extensions.Registry
	Paths    []string
	Errors   []LoadError
}

// Loader owns the build cache and one isolated Sobek VM per loaded extension.
type Loader struct {
	options DiscoveryOptions
	cache   *buildCache

	mu  sync.Mutex
	vms []*runtimeVM
}

func NewLoader(options DiscoveryOptions) *Loader {
	return &Loader{options: options, cache: newBuildCache()}
}

func (loader *Loader) Load(ctx context.Context) LoadResult {
	loader.mu.Lock()
	defer loader.mu.Unlock()
	return loader.loadLocked(ctx)
}

// Reload rebuilds changed bundles and always replaces every VM and registry.
func (loader *Loader) Reload(ctx context.Context) LoadResult {
	return loader.Load(ctx)
}

func (loader *Loader) loadLocked(ctx context.Context) LoadResult {
	loader.closeVMsLocked()
	paths := Discover(loader.options)
	registry := extensions.NewRegistry(absoluteOrDot(loader.options.CWD))
	result := LoadResult{Registry: registry, Paths: append([]string(nil), paths...)}
	for _, path := range paths {
		built, err := loader.cache.build(path)
		if err != nil {
			result.Errors = append(result.Errors, upstreamLoadError(path, err))
			continue
		}
		vm, err := newRuntimeVM(ctx, path, built, absoluteOrDot(loader.options.CWD))
		if err != nil {
			result.Errors = append(result.Errors, upstreamLoadError(path, err))
			continue
		}
		err = registry.Register(path, func(api extensions.API) error {
			return vm.initialize(ctx, api)
		})
		if err != nil {
			vm.Close()
			result.Errors = append(result.Errors, upstreamLoadError(path, stripRegistryLoadPrefix(path, err)))
			continue
		}
		loader.vms = append(loader.vms, vm)
	}
	return result
}

func upstreamLoadError(path string, err error) LoadError {
	if errors.Is(err, errInvalidFactory) {
		return LoadError{Path: path, Error: "Extension does not export a valid factory function: " + path}
	}
	return LoadError{Path: path, Error: "Failed to load extension: " + err.Error()}
}

func stripRegistryLoadPrefix(path string, err error) error {
	message := err.Error()
	prefix := "extensions: load " + path + ": "
	if strings.HasPrefix(message, prefix) {
		return errors.New(strings.TrimPrefix(message, prefix))
	}
	return err
}

func (loader *Loader) Close() {
	loader.mu.Lock()
	loader.closeVMsLocked()
	loader.mu.Unlock()
}

func (loader *Loader) closeVMsLocked() {
	for _, vm := range loader.vms {
		vm.Close()
	}
	loader.vms = nil
}
