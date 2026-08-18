package upstreamsync

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

func renderReport(result Result, date string) string {
	var report strings.Builder
	fmt.Fprintf(&report, "# Upstream sync report — %s\n\n", date)
	if result.Green {
		report.WriteString("Status: **GREEN**. Generated fixtures pass the complete Go conformance suite.\n\n")
	} else {
		report.WriteString("Status: **RED**. The pinned lock and committed fixtures were not promoted.\n\n")
	}
	report.WriteString("## Revisions\n\n")
	fmt.Fprintf(&report, "- Pinned: `%s` (%s), synced %s\n", result.Base.Commit, valueOr(result.Base.Version, "version unknown"), valueOr(result.Base.SyncedAt, "date unknown"))
	fmt.Fprintf(&report, "- Target: `%s` from `%s` (%s, %s)\n", result.TargetCommit, result.TargetRef, valueOr(result.TargetVersion, "version unknown"), valueOr(result.TargetDate, "date unknown"))
	if result.TargetSubject != "" {
		fmt.Fprintf(&report, "- Target subject: %s\n", markdownText(result.TargetSubject))
	}
	if result.Descendant {
		report.WriteString("- Ancestry: target descends from the pinned commit.\n")
	} else {
		report.WriteString("- Ancestry: **not promotable**; the target does not descend from the pinned commit.\n")
	}
	fmt.Fprintf(&report, "- Promotion: %s\n", result.Promotion)

	counts := map[string]int{ClassWire: 0, ClassAPI: 0, ClassFeature: 0, ClassDocs: 0}
	for _, change := range result.Changes {
		counts[change.Classification]++
	}
	report.WriteString("\n## Delta summary\n\n")
	fmt.Fprintf(&report, "%d upstream paths changed: %d kernel obligations (%d wire-format, %d API-surface) and %d optional (%d feature-only, %d docs).\n",
		len(result.Changes), counts[ClassWire]+counts[ClassAPI], counts[ClassWire], counts[ClassAPI],
		counts[ClassFeature]+counts[ClassDocs], counts[ClassFeature], counts[ClassDocs])
	if len(result.Changes) == 0 {
		report.WriteString("\nNo upstream source paths differ from the pinned commit.\n")
	} else {
		writeChangeTable(&report, "Kernel obligations (wire-format, API-surface)", result.Changes, func(class string) bool {
			return class == ClassWire || class == ClassAPI
		})
		writeChangeTable(&report, "Optional — cherry-picks on merit (feature-only, docs)", result.Changes, func(class string) bool {
			return class == ClassFeature || class == ClassDocs
		})
	}

	report.WriteString("\n## Fixture regeneration\n\n")
	fmt.Fprintf(&report, "Extraction: **%s**.\n", strings.ToUpper(result.Extraction.Status))
	if result.Extraction.Output != "" && result.Extraction.Status != "green" {
		writeOutput(&report, result.Extraction.Output)
	}
	if len(result.FixtureChanges) == 0 {
		report.WriteString("\nNo generated fixture bytes differ from the committed tree.\n")
	} else {
		report.WriteString("\n| Git | Fixture | Old bytes | New bytes | Old SHA-256 prefix | New SHA-256 prefix |\n")
		report.WriteString("|---|---|---:|---:|---|---|\n")
		for _, change := range result.FixtureChanges {
			fmt.Fprintf(&report, "| %s | %s | %d | %d | `%s` | `%s` |\n",
				change.Status, markdownCode(change.Path), change.OldBytes, change.NewBytes, change.OldHash, change.NewHash)
		}
	}

	report.WriteString("\n## Conformance\n\n")
	fmt.Fprintf(&report, "`go test -race ./...` against the generated fixture tree: **%s**.\n", strings.ToUpper(result.Conformance.Status))
	if result.Conformance.Output != "" {
		writeOutput(&report, result.Conformance.Output)
	}

	report.WriteString("\n## Proposed work items\n\n")
	items := proposedItems(result)
	if len(items) == 0 {
		report.WriteString("No follow-up work is proposed.\n")
	} else {
		for _, item := range items {
			fmt.Fprintf(&report, "- [ ] %s\n", item)
		}
	}
	return report.String()
}

func writeChangeTable(report *strings.Builder, title string, changes []Change, include func(string) bool) {
	fmt.Fprintf(report, "\n### %s\n\n", title)
	wroteHeader := false
	for _, change := range changes {
		if !include(change.Classification) {
			continue
		}
		if !wroteHeader {
			report.WriteString("| Git | Classification | Upstream path |\n|---|---|---|\n")
			wroteHeader = true
		}
		filename := change.Path
		if change.OldPath != "" && change.OldPath != change.Path {
			filename = change.OldPath + " → " + change.Path
		}
		fmt.Fprintf(report, "| %s | %s | %s |\n",
			markdownText(change.Status), markdownText(change.Classification), markdownCode(filename))
	}
	if !wroteHeader {
		report.WriteString("None.\n")
	}
}

func proposedItems(result Result) []string {
	classes := make(map[string]int)
	for _, change := range result.Changes {
		classes[change.Classification]++
	}
	var items []string
	if classes[ClassWire] > 0 {
		items = append(items, fmt.Sprintf("Audit %d wire-format path(s), port exact field and ordering changes, and regenerate every affected conformance family.", classes[ClassWire]))
	}
	if classes[ClassAPI] > 0 {
		items = append(items, fmt.Sprintf("Review %d public API path(s) for Go surface changes and add conformance coverage before promotion.", classes[ClassAPI]))
	}
	if classes[ClassFeature] > 0 {
		items = append(items, fmt.Sprintf("Optionally cherry-pick %d feature-only path(s) on merit; they carry no port obligation (P5).", classes[ClassFeature]))
	}
	if classes[ClassDocs] > 0 {
		items = append(items, fmt.Sprintf("Review %d documentation path(s) for newly specified kernel behavior.", classes[ClassDocs]))
	}
	if result.Extraction.Status == "red" {
		items = append(items, "Fix fixture extraction at the target revision before assessing Go conformance.")
	}
	if result.Conformance.Status == "red" {
		items = append(items, "Fix the reported conformance failures in Go; keep the generated upstream fixtures unchanged.")
	}
	return items
}

func writeOutput(report *strings.Builder, output string) {
	report.WriteString("\n````text\n")
	report.WriteString(truncateReportOutput(output, 16*1024))
	if !strings.HasSuffix(output, "\n") {
		report.WriteByte('\n')
	}
	report.WriteString("````\n")
}

func truncateReportOutput(output string, limit int) string {
	if len(output) <= limit {
		return output
	}
	half := limit / 2
	removed := len(output) - 2*half
	return output[:half] + "\n... " + strconv.Itoa(removed) + " bytes omitted ...\n" + output[len(output)-half:]
}

func writeReport(root, reportPath, report string, stdout io.Writer) (string, error) {
	if reportPath == "-" {
		if stdout == nil {
			stdout = os.Stdout
		}
		_, err := io.WriteString(stdout, report)
		return "-", err
	}
	if !filepath.IsAbs(reportPath) {
		reportPath = filepath.Join(root, reportPath)
	}
	if err := writeFileAtomic(reportPath, []byte(report), 0o644); err != nil {
		return "", fmt.Errorf("write sync report: %w", err)
	}
	return reportPath, nil
}

func markdownCode(value string) string {
	if value == "" {
		return "—"
	}
	value = strings.ReplaceAll(value, "\n", " ")
	value = strings.ReplaceAll(value, "|", "\\|")
	// Backslash escapes do not work inside code spans; drop backticks rather
	// than let one terminate the span and break the table.
	return "`" + strings.ReplaceAll(value, "`", "'") + "`"
}

func markdownText(value string) string {
	if value == "" {
		return "—"
	}
	value = strings.ReplaceAll(value, "|", "\\|")
	return strings.ReplaceAll(value, "\n", " ")
}

func valueOr(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}
