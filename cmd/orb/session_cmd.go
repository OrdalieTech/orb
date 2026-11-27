package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/OrdalieTech/orb/internal/nodepath"
	"github.com/OrdalieTech/orb/platforms/native/sqlite"
)

const sessionUsage = `Usage: orb session <command> [options]

Commands:
  list [--archived] [--all] [--cwd <dir>] [--match <substr>] [--limit N] [--json]
      List sessions (active by default).

  archive <id...> | --cwd <dir> | --match <substr> [--before <dur>] [--dry-run] [--json]
      Archive matching sessions. Requires at least one <id>, --cwd, --match, or --before.

  unarchive <id...> | --cwd <dir> | --match <substr> [--before <dur>] [--dry-run] [--json]
      Unarchive matching sessions. Requires at least one <id>, --cwd, --match, or --before.

  private <id...> [--off] [--json]
      Mark sessions as local-only (hidden from remote Mitosis peers). Use --off to clear.`

func runSessionCommand(ctx context.Context, argv []string, streams cliStreams) int {
	if len(argv) == 0 || argv[0] == "--help" || argv[0] == "-h" || argv[0] == "help" {
		_, _ = fmt.Fprintln(streams.Stdout, sessionUsage)
		return 0
	}
	state := stateFromContext(ctx)
	if state == nil {
		return reportCLIError(streams.Stderr, errors.New("orb session requires native SQLite storage (not supported with --pi-files)"))
	}
	var err error
	switch argv[0] {
	case "list":
		err = runSessionList(ctx, state.Sessions(), argv[1:], streams)
	case "archive":
		err = runSessionArchive(ctx, state.Sessions(), argv[1:], true, streams)
	case "unarchive":
		err = runSessionArchive(ctx, state.Sessions(), argv[1:], false, streams)
	case "private":
		err = runSessionPrivate(ctx, state.Sessions(), argv[1:], streams)
	default:
		err = fmt.Errorf("unknown session subcommand %q (run 'orb session --help')", argv[0])
	}
	if err != nil {
		return reportCLIError(streams.Stderr, err)
	}
	return 0
}

func runSessionList(ctx context.Context, sessions *sqlite.Sessions, argv []string, streams cliStreams) error {
	var (
		archived bool
		all      bool
		cwd      string
		match    string
		limit    int
		asJSON   bool
	)
	for i := 0; i < len(argv); i++ {
		arg := argv[i]
		switch {
		case arg == "--help" || arg == "-h":
			_, _ = fmt.Fprintln(streams.Stdout, sessionUsage)
			return nil
		case arg == "--archived":
			archived = true
		case arg == "--all":
			all = true
		case arg == "--json":
			asJSON = true
		case arg == "--cwd" && i+1 < len(argv):
			i++
			cwd = argv[i]
		case strings.HasPrefix(arg, "--cwd="):
			cwd = strings.TrimPrefix(arg, "--cwd=")
		case arg == "--match" && i+1 < len(argv):
			i++
			match = argv[i]
		case strings.HasPrefix(arg, "--match="):
			match = strings.TrimPrefix(arg, "--match=")
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
		default:
			return fmt.Errorf("unknown flag or argument %q", arg)
		}
	}
	resolvedCWD, err := resolveSessionCWD(cwd)
	if err != nil {
		return err
	}
	filter := sqlite.SessionFilter{
		CWD:   resolvedCWD,
		Match: match,
		Limit: limit,
	}
	if !all {
		a := archived
		filter.Archived = &a
	}
	entries, err := sessions.MatchSessions(ctx, filter)
	if err != nil {
		return err
	}
	if entries == nil {
		entries = []sqlite.CatalogEntry{}
	}
	if asJSON {
		return writeSessionJSON(streams.Stdout, entries)
	}
	if len(entries) == 0 {
		_, _ = fmt.Fprintln(streams.Stdout, "No sessions found.")
		return nil
	}
	for _, e := range entries {
		_, _ = fmt.Fprintln(streams.Stdout, formatSessionLine(e))
	}
	return nil
}

func runSessionArchive(ctx context.Context, sessions *sqlite.Sessions, argv []string, archive bool, streams cliStreams) error {
	var (
		ids    []string
		cwd    string
		match  string
		before string
		dryRun bool
		asJSON bool
	)
	for i := 0; i < len(argv); i++ {
		arg := argv[i]
		switch {
		case arg == "--help" || arg == "-h":
			_, _ = fmt.Fprintln(streams.Stdout, sessionUsage)
			return nil
		case arg == "--dry-run":
			dryRun = true
		case arg == "--json":
			asJSON = true
		case arg == "--cwd" && i+1 < len(argv):
			i++
			cwd = argv[i]
		case strings.HasPrefix(arg, "--cwd="):
			cwd = strings.TrimPrefix(arg, "--cwd=")
		case arg == "--match" && i+1 < len(argv):
			i++
			match = argv[i]
		case strings.HasPrefix(arg, "--match="):
			match = strings.TrimPrefix(arg, "--match=")
		case arg == "--before" && i+1 < len(argv):
			i++
			before = argv[i]
		case strings.HasPrefix(arg, "--before="):
			before = strings.TrimPrefix(arg, "--before=")
		case strings.HasPrefix(arg, "-"):
			return fmt.Errorf("unknown flag %q", arg)
		default:
			ids = append(ids, arg)
		}
	}

	verb := "archive"
	pastVerb := "Archived"
	if !archive {
		verb = "unarchive"
		pastVerb = "Unarchived"
	}
	if len(ids) == 0 && cwd == "" && match == "" && before == "" {
		return fmt.Errorf("refusing to %s all sessions without a filter; specify at least one <id>, --cwd, --match, or --before", verb)
	}

	resolvedCWD, err := resolveSessionCWD(cwd)
	if err != nil {
		return err
	}
	var beforeTime time.Time
	if before != "" {
		dur, err := parseHumanDuration(before)
		if err != nil {
			return fmt.Errorf("invalid --before %q: %w", before, err)
		}
		beforeTime = time.Now().UTC().Add(-dur)
	}

	wantArchived := !archive
	filter := sqlite.SessionFilter{
		IDs:      ids,
		CWD:      resolvedCWD,
		Match:    match,
		Before:   beforeTime,
		Archived: &wantArchived,
	}

	var matched []sqlite.CatalogEntry
	if dryRun {
		matched, err = sessions.MatchSessions(ctx, filter)
	} else {
		matched, err = sessions.ArchiveMatching(ctx, filter, archive)
	}
	if err != nil {
		return err
	}
	if matched == nil {
		matched = []sqlite.CatalogEntry{}
	}
	if asJSON {
		return writeSessionJSON(streams.Stdout, map[string]any{
			"archived": archive,
			"dry_run":  dryRun,
			"count":    len(matched),
			"sessions": matched,
		})
	}
	if len(matched) == 0 {
		_, _ = fmt.Fprintln(streams.Stdout, "No matching sessions.")
		return nil
	}
	for _, e := range matched {
		_, _ = fmt.Fprintln(streams.Stdout, formatSessionLine(e))
	}
	if dryRun {
		_, _ = fmt.Fprintf(streams.Stdout, "Would %s %d session(s).\n", verb, len(matched))
	} else {
		_, _ = fmt.Fprintf(streams.Stdout, "%s %d session(s).\n", pastVerb, len(matched))
	}
	return nil
}

func runSessionPrivate(ctx context.Context, sessions *sqlite.Sessions, argv []string, streams cliStreams) error {
	var (
		ids    []string
		off    bool
		asJSON bool
	)
	for i := 0; i < len(argv); i++ {
		arg := argv[i]
		switch {
		case arg == "--help" || arg == "-h":
			_, _ = fmt.Fprintln(streams.Stdout, sessionUsage)
			return nil
		case arg == "--off":
			off = true
		case arg == "--json":
			asJSON = true
		case strings.HasPrefix(arg, "-"):
			return fmt.Errorf("unknown flag %q", arg)
		default:
			ids = append(ids, arg)
		}
	}
	if len(ids) == 0 {
		return errors.New("specify at least one session <id>")
	}
	matched, err := sessions.MatchSessions(ctx, sqlite.SessionFilter{IDs: ids})
	if err != nil {
		return err
	}
	if len(matched) == 0 {
		return errors.New("no matching sessions found")
	}
	localOnly := !off
	for i := range matched {
		if err := sessions.SetSessionLocalOnly(ctx, matched[i].ID, localOnly); err != nil {
			return err
		}
		matched[i].LocalOnly = localOnly
	}
	if asJSON {
		return writeSessionJSON(streams.Stdout, map[string]any{
			"local_only": localOnly,
			"count":      len(matched),
			"sessions":   matched,
		})
	}
	for _, e := range matched {
		_, _ = fmt.Fprintln(streams.Stdout, formatSessionLine(e))
	}
	if localOnly {
		_, _ = fmt.Fprintf(streams.Stdout, "Marked %d session(s) as local-only.\n", len(matched))
	} else {
		_, _ = fmt.Fprintf(streams.Stdout, "Cleared local-only on %d session(s).\n", len(matched))
	}
	return nil
}

func resolveSessionCWD(cwd string) (string, error) {
	if cwd == "" {
		return "", nil
	}
	expanded, err := nodepath.Expand(cwd)
	if err != nil {
		return "", err
	}
	return filepath.Abs(expanded)
}

func parseHumanDuration(s string) (time.Duration, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, errors.New("empty duration")
	}
	if d, err := time.ParseDuration(s); err == nil {
		if d <= 0 {
			return 0, errors.New("duration must be positive")
		}
		return d, nil
	}
	var total time.Duration
	rest := s
	for len(rest) > 0 {
		i := 0
		for i < len(rest) && ((rest[i] >= '0' && rest[i] <= '9') || rest[i] == '.') {
			i++
		}
		if i == 0 || i == len(rest) {
			return 0, fmt.Errorf("invalid duration %q", s)
		}
		numStr := rest[:i]
		rest = rest[i:]
		j := 0
		for j < len(rest) && (rest[j] < '0' || rest[j] > '9') && rest[j] != '.' {
			j++
		}
		unit := rest[:j]
		rest = rest[j:]
		switch unit {
		case "w":
			val, err := strconv.ParseFloat(numStr, 64)
			if err != nil {
				return 0, fmt.Errorf("invalid duration %q", s)
			}
			total += time.Duration(val * float64(7*24*time.Hour))
		case "d":
			val, err := strconv.ParseFloat(numStr, 64)
			if err != nil {
				return 0, fmt.Errorf("invalid duration %q", s)
			}
			total += time.Duration(val * float64(24*time.Hour))
		default:
			part, err := time.ParseDuration(numStr + unit)
			if err != nil {
				return 0, fmt.Errorf("invalid duration %q", s)
			}
			total += part
		}
	}
	if total <= 0 {
		return 0, errors.New("duration must be positive")
	}
	return total, nil
}

func formatSessionLine(e sqlite.CatalogEntry) string {
	var flags []string
	if e.Archived {
		flags = append(flags, "archived")
	}
	if e.LocalOnly {
		flags = append(flags, "private")
	}
	flagStr := ""
	if len(flags) > 0 {
		flagStr = " [" + strings.Join(flags, ",") + "]"
	}
	label := e.Name
	if label == "" {
		label = strings.TrimSpace(strings.ReplaceAll(e.Preview, "\n", " "))
		runes := []rune(label)
		if len(runes) > 60 {
			label = string(runes[:60]) + "..."
		}
	}
	modified := e.Modified
	if t, err := time.Parse(time.RFC3339Nano, e.Modified); err == nil {
		modified = t.UTC().Format("2006-01-02 15:04")
	}
	return fmt.Sprintf("%s%s  %s  (%d msgs)  %s  %s", e.ID, flagStr, modified, e.MessageCount, e.CWD, label)
}

func writeSessionJSON(w interface{ Write([]byte) (int, error) }, v any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}
