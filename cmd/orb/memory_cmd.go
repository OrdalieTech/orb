package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/OrdalieTech/orb/agent/config"
	"github.com/OrdalieTech/orb/plugins/memory"
	"github.com/OrdalieTech/orb/plugins/memory/filestore"
)

const memoryUsage = `Usage: orb memory <command> [options]

Commands:
  status [--json]
      Show memory entry count, node count, profile usage, and view size/budget.

  view [--view-bytes N] [--json]
      Render the USER PROFILE and <memory_view> timeline.

  compact [--json]
      Materialize zero-LLM summary tree nodes and report how many were built.

  search <query> [--limit N] [--json]
      Lexical search across summary nodes and verbatim entries.

  zoom <id> [n] [--depth D] [--offset O] [--json]
      Inspect verbatim entry <id> or expand summary node <id>+<n>.`

func runMemoryCommand(ctx context.Context, argv []string, streams cliStreams) int {
	if len(argv) == 0 || argv[0] == "--help" || argv[0] == "-h" || argv[0] == "help" {
		_, _ = fmt.Fprintln(streams.Stdout, memoryUsage)
		return 0
	}
	store, err := openCLIMemoryStore(ctx)
	if err != nil {
		return reportCLIError(streams.Stderr, err)
	}
	switch argv[0] {
	case "status":
		err = runMemoryStatus(ctx, store, argv[1:], streams)
	case "view":
		err = runMemoryView(ctx, store, argv[1:], streams)
	case "compact":
		err = runMemoryCompact(ctx, store, argv[1:], streams)
	case "search":
		err = runMemorySearch(ctx, store, argv[1:], streams)
	case "zoom":
		err = runMemoryZoom(ctx, store, argv[1:], streams)
	default:
		err = fmt.Errorf("unknown memory subcommand %q (run 'orb memory --help')", argv[0])
	}
	if err != nil {
		return reportCLIError(streams.Stderr, err)
	}
	return 0
}

func openCLIMemoryStore(ctx context.Context) (memory.Store, error) {
	if state := stateFromContext(ctx); state != nil {
		return state.Memory(), nil
	}
	agentDir, err := config.GetAgentDir()
	if err != nil {
		return nil, err
	}
	return filestore.NewFileStore(filepath.Join(agentDir, "memory"))
}

type memoryStatusReport struct {
	Entries         int `json:"entries"`
	Nodes           int `json:"nodes"`
	ProfileEntries  int `json:"profile_entries"`
	ProfileChars    int `json:"profile_chars"`
	ProfileMaxChars int `json:"profile_max_chars"`
	ViewBytes       int `json:"view_bytes"`
	ViewBudgetBytes int `json:"view_budget_bytes"`
}

func runMemoryStatus(ctx context.Context, store memory.Store, argv []string, streams cliStreams) error {
	var asJSON bool
	for _, arg := range argv {
		switch arg {
		case "--help", "-h":
			_, _ = fmt.Fprintln(streams.Stdout, memoryUsage)
			return nil
		case "--json":
			asJSON = true
		default:
			return fmt.Errorf("unknown flag or argument %q", arg)
		}
	}
	entries, err := store.EntryCount(ctx)
	if err != nil {
		return err
	}
	nodes, err := store.ListNodes(ctx)
	if err != nil {
		return err
	}
	profile, err := store.GetProfile(ctx)
	if err != nil {
		return err
	}
	view, err := memory.BuildView(ctx, store, nil, memory.DefaultViewBytes)
	if err != nil {
		return err
	}
	report := memoryStatusReport{
		Entries:         entries,
		Nodes:           len(nodes),
		ProfileEntries:  len(profile),
		ProfileChars:    utf8.RuneCountInString(memory.RenderProfile(profile)),
		ProfileMaxChars: memory.ProfileMaxChars,
		ViewBytes:       view.Bytes,
		ViewBudgetBytes: view.HighBytes,
	}
	if asJSON {
		return writeMemoryJSON(streams.Stdout, report)
	}
	_, _ = fmt.Fprintf(streams.Stdout, "Entries: %d\nNodes: %d\nProfile: %d entries (%d/%d chars)\nView: %d/%d bytes\n",
		report.Entries, report.Nodes, report.ProfileEntries, report.ProfileChars, report.ProfileMaxChars, report.ViewBytes, report.ViewBudgetBytes)
	return nil
}

type memoryViewReport struct {
	Profile         string `json:"profile"`
	View            string `json:"view"`
	EntryCount      int    `json:"entry_count"`
	ViewBytes       int    `json:"view_bytes"`
	ViewBudgetBytes int    `json:"view_budget_bytes"`
}

func runMemoryView(ctx context.Context, store memory.Store, argv []string, streams cliStreams) error {
	var (
		viewBytes int
		asJSON    bool
	)
	for i := 0; i < len(argv); i++ {
		arg := argv[i]
		switch {
		case arg == "--help" || arg == "-h":
			_, _ = fmt.Fprintln(streams.Stdout, memoryUsage)
			return nil
		case arg == "--json":
			asJSON = true
		case arg == "--view-bytes" && i+1 < len(argv):
			i++
			n, err := strconv.Atoi(argv[i])
			if err != nil || n <= 0 {
				return fmt.Errorf("invalid --view-bytes %q", argv[i])
			}
			viewBytes = n
		case strings.HasPrefix(arg, "--view-bytes="):
			raw := strings.TrimPrefix(arg, "--view-bytes=")
			n, err := strconv.Atoi(raw)
			if err != nil || n <= 0 {
				return fmt.Errorf("invalid --view-bytes %q", raw)
			}
			viewBytes = n
		default:
			return fmt.Errorf("unknown flag or argument %q", arg)
		}
	}
	profile, err := store.GetProfile(ctx)
	if err != nil {
		return err
	}
	view, err := memory.BuildView(ctx, store, nil, viewBytes)
	if err != nil {
		return err
	}
	report := memoryViewReport{
		Profile:         memory.RenderProfile(profile),
		View:            view.Render(),
		EntryCount:      view.EntryCount,
		ViewBytes:       view.Bytes,
		ViewBudgetBytes: view.HighBytes,
	}
	if asJSON {
		return writeMemoryJSON(streams.Stdout, report)
	}
	profileBlock := report.Profile
	if profileBlock == "" {
		profileBlock = "(empty)"
	}
	_, _ = fmt.Fprintf(streams.Stdout, "USER PROFILE\n%s\n\n<memory_view>\n%s\n</memory_view>\n", profileBlock, report.View)
	return nil
}

type memoryCompactReport struct {
	BuiltNodes int `json:"built_nodes"`
}

func runMemoryCompact(ctx context.Context, store memory.Store, argv []string, streams cliStreams) error {
	var asJSON bool
	for _, arg := range argv {
		switch arg {
		case "--help", "-h":
			_, _ = fmt.Fprintln(streams.Stdout, memoryUsage)
			return nil
		case "--json":
			asJSON = true
		default:
			return fmt.Errorf("unknown flag or argument %q", arg)
		}
	}
	built, err := memory.MaterializeFreeNodes(ctx, store)
	if err != nil {
		return err
	}
	if asJSON {
		return writeMemoryJSON(streams.Stdout, memoryCompactReport{BuiltNodes: built})
	}
	_, _ = fmt.Fprintf(streams.Stdout, "Compacted %d node(s).\n", built)
	return nil
}

func runMemorySearch(ctx context.Context, store memory.Store, argv []string, streams cliStreams) error {
	var (
		limit  int
		asJSON bool
		terms  []string
	)
	for i := 0; i < len(argv); i++ {
		arg := argv[i]
		switch {
		case arg == "--help" || arg == "-h":
			_, _ = fmt.Fprintln(streams.Stdout, memoryUsage)
			return nil
		case arg == "--json":
			asJSON = true
		case arg == "--limit" && i+1 < len(argv):
			i++
			n, err := strconv.Atoi(argv[i])
			if err != nil || n < 0 {
				return fmt.Errorf("invalid --limit %q", argv[i])
			}
			limit = n
		case strings.HasPrefix(arg, "--limit="):
			raw := strings.TrimPrefix(arg, "--limit=")
			n, err := strconv.Atoi(raw)
			if err != nil || n < 0 {
				return fmt.Errorf("invalid --limit %q", raw)
			}
			limit = n
		case strings.HasPrefix(arg, "-"):
			return fmt.Errorf("unknown flag %q", arg)
		default:
			terms = append(terms, arg)
		}
	}
	query := strings.TrimSpace(strings.Join(terms, " "))
	if query == "" {
		return errors.New("search requires a <query>")
	}
	hits, err := memory.Search(ctx, store, query, limit)
	if err != nil {
		return err
	}
	if hits == nil {
		hits = []memory.SearchHit{}
	}
	if asJSON {
		return writeMemoryJSON(streams.Stdout, hits)
	}
	if len(hits) == 0 {
		_, _ = fmt.Fprintln(streams.Stdout, "No matches found.")
		return nil
	}
	for _, hit := range hits {
		_, _ = fmt.Fprintf(streams.Stdout, "%s  %s\n", hit.Ref, hit.Snippet)
	}
	return nil
}

type memoryZoomReport struct {
	Ref  string `json:"ref"`
	ID   int    `json:"id"`
	Span int    `json:"span"`
	Text string `json:"text"`
}

func runMemoryZoom(ctx context.Context, store memory.Store, argv []string, streams cliStreams) error {
	var (
		depth  int
		offset int
		asJSON bool
		pos    []string
	)
	for i := 0; i < len(argv); i++ {
		arg := argv[i]
		switch {
		case arg == "--help" || arg == "-h":
			_, _ = fmt.Fprintln(streams.Stdout, memoryUsage)
			return nil
		case arg == "--json":
			asJSON = true
		case arg == "--depth" && i+1 < len(argv):
			i++
			n, err := strconv.Atoi(argv[i])
			if err != nil || n < 0 {
				return fmt.Errorf("invalid --depth %q", argv[i])
			}
			depth = n
		case strings.HasPrefix(arg, "--depth="):
			raw := strings.TrimPrefix(arg, "--depth=")
			n, err := strconv.Atoi(raw)
			if err != nil || n < 0 {
				return fmt.Errorf("invalid --depth %q", raw)
			}
			depth = n
		case arg == "--offset" && i+1 < len(argv):
			i++
			n, err := strconv.Atoi(argv[i])
			if err != nil || n < 0 {
				return fmt.Errorf("invalid --offset %q", argv[i])
			}
			offset = n
		case strings.HasPrefix(arg, "--offset="):
			raw := strings.TrimPrefix(arg, "--offset=")
			n, err := strconv.Atoi(raw)
			if err != nil || n < 0 {
				return fmt.Errorf("invalid --offset %q", raw)
			}
			offset = n
		case strings.HasPrefix(arg, "-"):
			return fmt.Errorf("unknown flag %q", arg)
		default:
			pos = append(pos, arg)
		}
	}
	if len(pos) == 0 || len(pos) > 2 {
		return errors.New("zoom requires <id> [n] or <id>+<n>")
	}
	var id, n int
	if len(pos) == 1 {
		if parsedID, parsedN, ok := memory.ParseRef(pos[0]); ok {
			id, n = parsedID, parsedN
		} else {
			parsedID, err := strconv.Atoi(pos[0])
			if err != nil || parsedID < 0 {
				return fmt.Errorf("invalid memory reference %q", pos[0])
			}
			id, n = parsedID, 1
		}
	} else {
		parsedID, err := strconv.Atoi(pos[0])
		if err != nil || parsedID < 0 {
			return fmt.Errorf("invalid entry id %q", pos[0])
		}
		parsedN, err := strconv.Atoi(pos[1])
		if err != nil || parsedN < 1 {
			return fmt.Errorf("invalid span %q", pos[1])
		}
		id, n = parsedID, parsedN
	}
	text, err := memory.Zoom(ctx, store, id, n, depth, offset)
	if err != nil {
		return err
	}
	if asJSON {
		return writeMemoryJSON(streams.Stdout, memoryZoomReport{
			Ref:  fmt.Sprintf("%d+%d", id, n),
			ID:   id,
			Span: n,
			Text: text,
		})
	}
	_, _ = fmt.Fprintln(streams.Stdout, text)
	return nil
}

func writeMemoryJSON(w io.Writer, value any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(value)
}
