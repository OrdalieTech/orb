package upstreamsync

import (
	"context"
	"errors"
	"io"
	"time"
)

const (
	ClassWire    = "wire-format"
	ClassAPI     = "API-surface"
	ClassFeature = "feature-only"
	ClassDocs    = "docs"
)

var (
	ErrRed             = errors.New("upstream sync is red")
	ErrPromotionUnsafe = errors.New("upstream lock promotion refused")
)

// Config controls one upstream analysis or promotion run.
type Config struct {
	Root        string
	UpstreamDir string
	Target      string
	ReportPath  string
	Fetch       bool
	DryRun      bool
	Bump        bool
	Now         time.Time
	Stdout      io.Writer

	generate    generateFunc
	conformance conformanceFunc
}

type Lock struct {
	Repo     string `json:"repo"`
	Commit   string `json:"commit"`
	Version  string `json:"version"`
	SyncedAt string `json:"syncedAt"`
}

type Change struct {
	Status         string
	OldPath        string
	Path           string
	Classification string
	Targets        []string
	WPs            []string
}

type FixtureChange struct {
	Status   string
	Path     string
	OldBytes int64
	NewBytes int64
	OldHash  string
	NewHash  string
}

type Check struct {
	Status string
	Output string
}

type Result struct {
	Base              Lock
	TargetCommit      string
	TargetRef         string
	TargetVersion     string
	TargetDate        string
	TargetSubject     string
	Descendant        bool
	Changes           []Change
	FixtureChanges    []FixtureChange
	Extraction        Check
	Conformance       Check
	Green             bool
	Promotion         string
	Report            string
	ReportPath        string
	UnmappedPathCount int
}

type generateFunc func(context.Context, string, string, string, string) (string, error)
type conformanceFunc func(context.Context, string, string) (string, error)
