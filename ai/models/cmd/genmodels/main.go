package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"

	"github.com/OrdalieTech/pi-go/ai/models/internal/cataloggen"
)

func main() {
	input := flag.String("input", "", "models.dev api.json snapshot (fetches -url when empty)")
	endpoint := flag.String("url", "https://models.dev/api.json", "models.dev endpoint")
	output := flag.String("output", "generated.go", "generated Go file")
	flag.Parse()
	data, err := readInput(*input, *endpoint)
	if err != nil {
		fatal(err)
	}
	formatted, err := cataloggen.Render(data)
	if err != nil {
		fatal(err)
	}
	if err := os.WriteFile(*output, formatted, 0o644); err != nil {
		fatal(err)
	}
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
		return nil, fmt.Errorf("models.dev request failed: %s", response.Status)
	}
	return io.ReadAll(response.Body)
}

func fatal(err error) {
	_, _ = fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}
