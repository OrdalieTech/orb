package jsbridge

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/evanw/esbuild/pkg/api"
)

type artifact struct {
	code []byte
}

type buildRecord struct {
	digest string
	inputs []string
}

type buildCache struct {
	mu        sync.Mutex
	entries   map[string]buildRecord
	artifacts map[string]artifact
	builds    int
}

func newBuildCache() *buildCache {
	return &buildCache{
		entries:   make(map[string]buildRecord),
		artifacts: make(map[string]artifact),
	}
}

func (cache *buildCache) build(entry string) (artifact, error) {
	cache.mu.Lock()
	defer cache.mu.Unlock()

	entry = filepath.Clean(entry)
	if record, ok := cache.entries[entry]; ok {
		if digest, err := hashInputs(record.inputs); err == nil && digest == record.digest {
			if cached, exists := cache.artifacts[digest]; exists {
				return cloneArtifact(cached), nil
			}
		}
	}

	result := api.Build(api.BuildOptions{
		AbsWorkingDir:  filepath.Dir(entry),
		EntryPoints:    []string{entry},
		Bundle:         true,
		Write:          false,
		Outfile:        "extension.js",
		Format:         api.FormatCommonJS,
		Platform:       api.PlatformNode,
		Target:         api.ES2017,
		Sourcemap:      api.SourceMapInline,
		SourcesContent: api.SourcesContentInclude,
		Metafile:       true,
		Charset:        api.CharsetUTF8,
		LegalComments:  api.LegalCommentsNone,
		LogLevel:       api.LogLevelSilent,
		Plugins:        []api.Plugin{importMetaPlugin(), packageImportsExtensionPlugin()},
		// CommonJS output leaves import.meta empty; upstream runs each source
		// as ESM, so preserve module-local metadata before bundling.
		Define: map[string]string{
			"import.meta.url":      importMetaBinding + ".url",
			"import.meta.filename": importMetaBinding + ".filename",
			"import.meta.dirname":  importMetaBinding + ".dirname",
		},
		External: []string{
			"pi",
			"typebox",
			"typebox/*",
			"@sinclair/typebox",
			"@sinclair/typebox/*",
			"@earendil-works/pi-*",
			"@mariozechner/pi-*",
		},
	})
	cache.builds++
	if len(result.Errors) > 0 {
		return artifact{}, formatBuildError(result.Errors)
	}
	if len(result.OutputFiles) != 1 {
		return artifact{}, fmt.Errorf("esbuild produced %d artifacts", len(result.OutputFiles))
	}

	inputs, err := metafileInputs(result.Metafile, filepath.Dir(entry), entry)
	if err != nil {
		return artifact{}, err
	}
	digest, err := hashInputs(inputs)
	if err != nil {
		return artifact{}, err
	}
	built := artifact{code: append([]byte(nil), result.OutputFiles[0].Contents...)}
	cache.entries[entry] = buildRecord{digest: digest, inputs: inputs}
	cache.artifacts[digest] = built
	return cloneArtifact(built), nil
}

func cloneArtifact(value artifact) artifact {
	return artifact{code: append([]byte(nil), value.code...)}
}

func metafileInputs(metafile, workingDir, entry string) ([]string, error) {
	var decoded struct {
		Inputs map[string]json.RawMessage `json:"inputs"`
	}
	if err := json.Unmarshal([]byte(metafile), &decoded); err != nil {
		return nil, fmt.Errorf("decode esbuild metafile: %w", err)
	}
	seen := map[string]struct{}{filepath.Clean(entry): {}}
	for input := range decoded.Inputs {
		if strings.HasPrefix(input, "<") || strings.HasPrefix(input, "pigo-import-meta:") {
			continue
		}
		if !filepath.IsAbs(input) {
			input = filepath.Join(workingDir, filepath.FromSlash(input))
		}
		seen[filepath.Clean(input)] = struct{}{}
	}
	// Esbuild's metafile omits package and TypeScript configuration even though
	// those files can change resolution or output, so they are cache inputs too.
	for input := range seen {
		for directory := filepath.Dir(input); ; directory = filepath.Dir(directory) {
			for _, name := range []string{"package.json", "tsconfig.json", "jsconfig.json"} {
				candidate := filepath.Join(directory, name)
				if info, err := os.Stat(candidate); err == nil && info.Mode().IsRegular() {
					seen[candidate] = struct{}{}
				}
			}
			parent := filepath.Dir(directory)
			if parent == directory {
				break
			}
		}
	}
	inputs := make([]string, 0, len(seen))
	for input := range seen {
		inputs = append(inputs, input)
	}
	sort.Strings(inputs)
	return inputs, nil
}

func hashInputs(inputs []string) (string, error) {
	hash := sha256.New()
	for _, input := range inputs {
		content, err := os.ReadFile(input)
		if err != nil {
			return "", err
		}
		hash.Write([]byte(input))
		hash.Write([]byte{0})
		hash.Write(content)
		hash.Write([]byte{0})
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func entryFileURL(entry string) string {
	return (&url.URL{Scheme: "file", Path: entry}).String()
}

func formatBuildError(messages []api.Message) error {
	parts := make([]string, 0, len(messages))
	for _, message := range messages {
		text := clarifyBuildMessage(message.Text)
		if message.Location == nil {
			parts = append(parts, text)
			continue
		}
		parts = append(parts, fmt.Sprintf(
			"%s:%d:%d: %s",
			message.Location.File,
			message.Location.Line,
			message.Location.Column+1,
			text,
		))
	}
	return fmt.Errorf("%s", strings.Join(parts, "\n"))
}

// clarifyBuildMessage turns esbuild's generic loader errors for artifacts the
// runtime can never execute into actionable diagnostics.
func clarifyBuildMessage(text string) string {
	switch {
	case strings.Contains(text, `No loader is configured for ".node" files`):
		return text + " (native Node addons are not supported by the pigo extension runtime)"
	case strings.Contains(text, `No loader is configured for ".wasm" files`):
		return text + " (WebAssembly modules are not supported by the pigo extension runtime)"
	default:
		return text
	}
}
