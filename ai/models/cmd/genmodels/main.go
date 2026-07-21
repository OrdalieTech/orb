package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/OrdalieTech/pigo/ai/models/internal/cataloggen"
)

func main() {
	input := flag.String("input", "", "models.dev api.json snapshot (fetches -url when empty)")
	endpoint := flag.String("url", "https://models.dev/api.json", "models.dev endpoint")
	nimInput := flag.String("nim", "", "NVIDIA NIM /v1/models snapshot (fetches -nim-url when empty)")
	nimEndpoint := flag.String("nim-url", "https://integrate.api.nvidia.com/v1/models", "NVIDIA NIM models endpoint")
	openRouterInput := flag.String("openrouter", "", "OpenRouter /api/v1/models snapshot (fetches -openrouter-url when empty)")
	openRouterEndpoint := flag.String("openrouter-url", "https://openrouter.ai/api/v1/models", "OpenRouter models endpoint")
	vercelInput := flag.String("vercel", "", "Vercel AI Gateway /v1/models snapshot (fetches -vercel-url when empty)")
	vercelEndpoint := flag.String("vercel-url", "https://ai-gateway.vercel.sh/v1/models", "Vercel AI Gateway models endpoint")
	generatedAt := flag.String("generated-at", "", "catalog build time, RFC 3339 (defaults to now; pin for deterministic output)")
	output := flag.String("output", "generated.go", "generated Go file")
	flag.Parse()
	sources := cataloggen.Sources{GeneratedAt: time.Now().UTC()}
	if *generatedAt != "" {
		parsed, err := time.Parse(time.RFC3339, *generatedAt)
		if err != nil {
			fatal(fmt.Errorf("parse -generated-at: %w", err))
		}
		sources.GeneratedAt = parsed
	}
	var err error
	if sources.ModelsDev, err = readInput(*input, *endpoint); err != nil {
		fatal(err)
	}
	if sources.NvidiaNIM, err = readInput(*nimInput, *nimEndpoint); err != nil {
		if *nimInput != "" {
			fatal(err)
		}
		// Loud fallback: without the live NIM listing the generator keeps only
		// the curated snapshot of NIM-served models baked into cataloggen.
		fmt.Fprintf(os.Stderr, "warning: NVIDIA NIM listing unavailable (%v); falling back to the curated NIM snapshot\n", err)
		sources.NvidiaNIM = nil
	}
	if sources.OpenRouter, err = readInput(*openRouterInput, *openRouterEndpoint); err != nil {
		fatal(fmt.Errorf("OpenRouter listing is required (pass -openrouter with a snapshot): %w", err))
	}
	if sources.Vercel, err = readInput(*vercelInput, *vercelEndpoint); err != nil {
		fatal(fmt.Errorf("vercel AI Gateway listing is required (pass -vercel with a snapshot): %w", err))
	}
	formatted, err := cataloggen.Render(sources)
	if err != nil {
		fatal(err)
	}
	if err := writeGeneratedFile(*output, formatted); err != nil {
		fatal(err)
	}
}

func writeGeneratedFile(path string, content []byte) (err error) {
	return writeGeneratedFileWithRename(path, content, os.Rename)
}

func writeGeneratedFileWithRename(path string, content []byte, rename func(string, string) error) (err error) {
	directory := filepath.Dir(path)
	temporary, err := os.CreateTemp(directory, "."+filepath.Base(path)+".*")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer func() { _ = os.Remove(temporaryPath) }()
	defer func() {
		if temporary != nil {
			_ = temporary.Close()
		}
	}()
	if err := temporary.Chmod(0o644); err != nil {
		return err
	}
	if _, err := temporary.Write(content); err != nil {
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	temporary = nil
	return rename(temporaryPath, path)
}

func readInput(path, endpoint string) ([]byte, error) {
	if path != "" {
		return os.ReadFile(path)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return nil, err
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		_, _ = io.Copy(io.Discard, response.Body)
		return nil, fmt.Errorf("%s request failed: %s", endpoint, response.Status)
	}
	return io.ReadAll(response.Body)
}

func fatal(err error) {
	_, _ = fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}
