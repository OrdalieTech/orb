package jsbridge

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/evanw/esbuild/pkg/api"
)

const (
	importMetaBinding = "__pigo_import_meta_81e4"
	importMetaModule  = "pigo-internal:import-meta"
)

func importMetaPlugin() api.Plugin {
	return api.Plugin{
		Name: "pi-import-meta",
		Setup: func(build api.PluginBuild) {
			build.OnLoad(api.OnLoadOptions{Filter: "\\.[cm]?[jt]sx?$", Namespace: "file"}, func(args api.OnLoadArgs) (api.OnLoadResult, error) {
				contents, err := os.ReadFile(args.Path)
				if err != nil {
					return api.OnLoadResult{}, err
				}
				loader, ok := scriptLoader(args.Path)
				if !ok {
					return api.OnLoadResult{}, nil
				}
				source := string(contents)
				if strings.Contains(source, "import.meta") {
					source += "\nimport * as " + importMetaBinding + " from " + strconv.Quote(importMetaModule) + ";\n"
				}
				return api.OnLoadResult{
					Contents:   &source,
					Loader:     loader,
					ResolveDir: filepath.Dir(args.Path),
					WatchFiles: []string{args.Path},
				}, nil
			})
			build.OnResolve(api.OnResolveOptions{Filter: "^pigo-internal:import-meta$"}, func(args api.OnResolveArgs) (api.OnResolveResult, error) {
				return api.OnResolveResult{
					Path:        filepath.Clean(args.Importer),
					Namespace:   "pigo-import-meta",
					SideEffects: api.SideEffectsFalse,
				}, nil
			})
			build.OnLoad(api.OnLoadOptions{Filter: ".*", Namespace: "pigo-import-meta"}, func(args api.OnLoadArgs) (api.OnLoadResult, error) {
				filename := filepath.Clean(args.Path)
				source := fmt.Sprintf(
					"export const url=%s;export const filename=%s;export const dirname=%s;",
					strconv.Quote(entryFileURL(filename)),
					strconv.Quote(filename),
					strconv.Quote(filepath.Dir(filename)),
				)
				return api.OnLoadResult{Contents: &source, Loader: api.LoaderJS}, nil
			})
		},
	}
}

func scriptLoader(path string) (api.Loader, bool) {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".js", ".mjs", ".cjs":
		return api.LoaderJS, true
	case ".jsx":
		return api.LoaderJSX, true
	case ".ts", ".mts", ".cts":
		return api.LoaderTS, true
	case ".tsx":
		return api.LoaderTSX, true
	default:
		return api.LoaderNone, false
	}
}
