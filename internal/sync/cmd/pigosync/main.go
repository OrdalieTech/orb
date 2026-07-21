package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"

	upstreamsync "github.com/OrdalieTech/pigo/internal/sync"
)

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	flags := flag.NewFlagSet("pigosync", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	var config upstreamsync.Config
	var noFetch bool
	flags.StringVar(&config.Root, "repo-root", "", "pigo repository root (auto-detected by default)")
	flags.StringVar(&config.UpstreamDir, "upstream-dir", "", "upstream checkout (default: <root>/.upstream)")
	flags.StringVar(&config.Target, "target", "origin/main", "upstream ref or commit to analyze")
	flags.StringVar(&config.ReportPath, "report", "", "report path relative to the repository, or - for stdout")
	flags.BoolVar(&config.DryRun, "dry-run", false, "analyze without promoting fixtures or the lock")
	flags.BoolVar(&config.Bump, "bump", false, "promote generated fixtures and the lock only when green")
	flags.BoolVar(&noFetch, "no-fetch", false, "use existing upstream refs without fetching")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if flags.NArg() != 0 {
		fmt.Fprintf(os.Stderr, "pigosync: unexpected arguments: %v\n", flags.Args())
		return 2
	}
	if !config.Bump {
		config.DryRun = true
	}
	config.Fetch = !noFetch
	config.Stdout = os.Stdout
	result, err := upstreamsync.Run(context.Background(), config)
	if result.ReportPath != "" && result.ReportPath != "-" {
		fmt.Fprintf(os.Stderr, "sync report: %s\n", result.ReportPath)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "pigosync: %v\n", err)
		return 1
	}
	return 0
}
