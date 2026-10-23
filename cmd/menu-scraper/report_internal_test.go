package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/gosom/kit/logging"
	"github.com/stretchr/testify/require"

	"github.com/gosom/google-maps-scraper/gmaps"
)

func reportFixture() (report *runReport, entries []*gmaps.Entry) {
	entries = []*gmaps.Entry{
		{Title: "Found", Cid: "1"},
		{Title: "Blocked", Cid: "2"},
		{Title: "No menu", Cid: "3"},
		{Title: "Unreached", Cid: "4"},
		{Title: "", Cid: "5"},
		{Title: "Skipped", Cid: "6"},
	}

	report = newRunReport()
	report.record(entries[0], gmaps.MenuFound)
	report.record(entries[1], gmaps.MenuBlocked)
	report.record(entries[2], gmaps.MenuNotFound)
	report.record(entries[5], gmaps.MenuSkipped)

	return report, entries
}

func TestRunReportSummarize(t *testing.T) {
	report, entries := reportFixture()

	counts, retry := report.summarize(entries)

	require.Equal(t, map[gmaps.MenuOutcome]int{
		gmaps.MenuFound:     1,
		gmaps.MenuBlocked:   1,
		gmaps.MenuNotFound:  1,
		outcomeNotProcessed: 1,
		outcomeNoTitle:      1,
		gmaps.MenuSkipped:   1,
	}, counts)
	require.Equal(t, []*gmaps.Entry{entries[1], entries[3], entries[5]}, retry, "retry keeps input order")
}

// recordingLogger keeps Info and Warn lines; other methods are unused here.
type recordingLogger struct {
	logging.Logger

	lines []logLine
}

type logLine struct {
	level  string
	msg    string
	fields map[string]any
}

func (l *recordingLogger) Info(msg string, args ...any) { l.add("info", msg, args) }

func (l *recordingLogger) Warn(msg string, args ...any) { l.add("warn", msg, args) }

func (l *recordingLogger) add(level, msg string, args []any) {
	fields := map[string]any{}

	for i := 0; i+1 < len(args); i += 2 {
		key, _ := args[i].(string)
		fields[key] = args[i+1]
	}

	l.lines = append(l.lines, logLine{level: level, msg: msg, fields: fields})
}

func TestReportRunWritesRetryFile(t *testing.T) {
	report, entries := reportFixture()
	failedPath := filepath.Join(t.TempDir(), "menu.failed.json")
	logger := &recordingLogger{}

	require.NoError(t, reportRun(logger, report, entries, failedPath))

	require.Len(t, logger.lines, 1)
	require.Equal(t, logLine{level: "info", msg: "menu summary", fields: map[string]any{
		"total":         6,
		"found":         1,
		"no_menu":       1,
		"mismatch":      0,
		"blocked":       1,
		"no_panel":      0,
		"fetch_failed":  0,
		"parse_failed":  0,
		"skipped":       1,
		"not_processed": 1,
		"no_title":      1,
		"retry":         3,
		"retry_file":    failedPath,
	}}, logger.lines[0])

	f, err := os.Open(failedPath)
	require.NoError(t, err)

	defer f.Close()

	retried, err := readEntries(f)
	require.NoError(t, err)
	require.Len(t, retried, 3)
	require.Equal(t, "Blocked", retried[0].Title)
	require.Equal(t, "4", retried[1].Cid)
}

func TestReportRunNothingToRetry(t *testing.T) {
	entries := []*gmaps.Entry{{Title: "Found"}}
	report := newRunReport()
	report.record(entries[0], gmaps.MenuFound)

	failedPath := filepath.Join(t.TempDir(), "menu.failed.json")
	logger := &recordingLogger{}

	require.NoError(t, reportRun(logger, report, entries, failedPath))

	require.Len(t, logger.lines, 1)
	require.Equal(t, 0, logger.lines[0].fields["retry"])
	require.Empty(t, logger.lines[0].fields["retry_file"])
	require.NoFileExists(t, failedPath)
}

func TestReportRunRemovesStaleRetryFile(t *testing.T) {
	entries := []*gmaps.Entry{{Title: "Found"}}
	report := newRunReport()
	report.record(entries[0], gmaps.MenuFound)

	failedPath := filepath.Join(t.TempDir(), "menu.failed.json")
	require.NoError(t, os.WriteFile(failedPath, []byte(`{"title":"Old failure"}`+"\n"), 0o600))

	require.NoError(t, reportRun(&recordingLogger{}, report, entries, failedPath))

	require.NoFileExists(t, failedPath, "a retry file from an earlier run must not survive a clean run")
}

func TestReportRunRemoveFailure(t *testing.T) {
	entries := []*gmaps.Entry{{Title: "Found"}}
	report := newRunReport()
	report.record(entries[0], gmaps.MenuFound)

	// A non-empty directory cannot be removed with os.Remove.
	failedPath := filepath.Join(t.TempDir(), "menu.failed.json")
	require.NoError(t, os.MkdirAll(filepath.Join(failedPath, "child"), 0o750))

	logger := &recordingLogger{}

	err := reportRun(logger, report, entries, failedPath)

	require.ErrorContains(t, err, "remove stale retry file")
	require.Len(t, logger.lines, 1, "the summary is still logged")
}

func TestReportRunWithoutFailedPath(t *testing.T) {
	report, entries := reportFixture()
	logger := &recordingLogger{}

	require.NoError(t, reportRun(logger, report, entries, ""))

	require.Len(t, logger.lines, 2)
	require.Empty(t, logger.lines[0].fields["retry_file"])
	require.Equal(t, "warn", logger.lines[1].level)
	require.Equal(t, 3, logger.lines[1].fields["retry"])
}

func TestReportRunWriteFailure(t *testing.T) {
	report, entries := reportFixture()
	logger := &recordingLogger{}
	failedPath := filepath.Join(t.TempDir(), "missing-dir", "menu.failed.json")

	err := reportRun(logger, report, entries, failedPath)

	require.ErrorContains(t, err, "write places to retry")
	require.Len(t, logger.lines, 1, "the summary is still logged")
	require.Empty(t, logger.lines[0].fields["retry_file"])
}

func TestDefaultFailedPath(t *testing.T) {
	require.Equal(t, "/out/menu.failed.json", defaultFailedPath("/out/menu.json"))
	require.Equal(t, "/out/menu.failed.json", defaultFailedPath("/out/menu.csv"))
	require.Equal(t, "menu.failed.json", defaultFailedPath("menu"))
	require.Empty(t, defaultFailedPath("stdout"))
}
