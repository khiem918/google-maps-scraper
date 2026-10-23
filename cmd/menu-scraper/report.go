package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/gosom/kit/logging"

	"github.com/gosom/google-maps-scraper/gmaps"
)

const (
	// outcomeNoTitle marks input places that could not be searched for.
	outcomeNoTitle gmaps.MenuOutcome = "no_title"
	// outcomeNotProcessed marks places the run stopped before reaching
	// (interrupted or exited on inactivity).
	outcomeNotProcessed gmaps.MenuOutcome = "not_processed"
)

// summaryOrder is the order outcomes appear in the summary log line.
var summaryOrder = []gmaps.MenuOutcome{
	gmaps.MenuFound,
	gmaps.MenuNotFound,
	gmaps.MenuMismatch,
	gmaps.MenuBlocked,
	gmaps.MenuNoPanel,
	gmaps.MenuFetchFailed,
	gmaps.MenuParseFailed,
	gmaps.MenuSkipped,
	outcomeNotProcessed,
	outcomeNoTitle,
}

// runReport collects the outcome of every place in a run.
type runReport struct {
	mu       sync.Mutex
	outcomes map[*gmaps.Entry]gmaps.MenuOutcome
}

func newRunReport() *runReport {
	return &runReport{outcomes: map[*gmaps.Entry]gmaps.MenuOutcome{}}
}

// record stores how the menu search of entry ended. MenuJobs call it from
// scrapemate workers.
func (r *runReport) record(entry *gmaps.Entry, outcome gmaps.MenuOutcome) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.outcomes[entry] = outcome
}

// summarize counts outcomes over entries and returns, in input order, the
// places worth searching again: retryable failures and places the run never
// reached.
func (r *runReport) summarize(entries []*gmaps.Entry) (counts map[gmaps.MenuOutcome]int, retry []*gmaps.Entry) {
	r.mu.Lock()
	defer r.mu.Unlock()

	counts = map[gmaps.MenuOutcome]int{}

	for _, entry := range entries {
		outcome, ok := r.outcomes[entry]

		switch {
		case entry.Title == "":
			outcome = outcomeNoTitle
		case !ok:
			outcome = outcomeNotProcessed
		}

		counts[outcome]++

		if outcome == outcomeNotProcessed || outcome.Retryable() {
			retry = append(retry, entry)
		}
	}

	return counts, retry
}

// summaryFields returns the key/value pairs of the summary log line, with
// every outcome present so the line has a fixed shape.
func summaryFields(total int, counts map[gmaps.MenuOutcome]int, retry int, retryFile string) []any {
	fields := []any{"total", total}

	for _, outcome := range summaryOrder {
		fields = append(fields, string(outcome), counts[outcome])
	}

	return append(fields, "retry", retry, "retry_file", retryFile)
}

// defaultFailedPath derives the retry file from the results path:
// out/menu.json becomes out/menu.failed.json. Results on stdout get none.
func defaultFailedPath(results string) string {
	if results == "" || results == "stdout" {
		return ""
	}

	return strings.TrimSuffix(results, filepath.Ext(results)) + ".failed.json"
}

// writeEntries writes entries as JSON lines, the input format of -input.
func writeEntries(path string, entries []*gmaps.Entry) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}

	w := bufio.NewWriter(f)
	enc := json.NewEncoder(w)

	for _, entry := range entries {
		if err := enc.Encode(entry); err != nil {
			_ = f.Close()

			return err
		}
	}

	if err := w.Flush(); err != nil {
		_ = f.Close()

		return err
	}

	return f.Close()
}

// reportRun writes the places to retry to failedPath, or removes a retry file
// left there by an earlier run when nothing is left to retry, and logs the run
// summary as one line in the scrapemate log format.
func reportRun(logger logging.Logger, report *runReport, entries []*gmaps.Entry, failedPath string) error {
	counts, retry := report.summarize(entries)

	var (
		retryFile string
		fileErr   error
	)

	switch {
	case failedPath == "":
	case len(retry) > 0:
		if err := writeEntries(failedPath, retry); err != nil {
			fileErr = fmt.Errorf("write places to retry: %w", err)
		} else {
			retryFile = failedPath
		}
	default:
		// A stale retry file would send places that are done to be searched again.
		if err := os.Remove(failedPath); err != nil && !errors.Is(err, fs.ErrNotExist) {
			fileErr = fmt.Errorf("remove stale retry file: %w", err)
		}
	}

	logger.Info("menu summary", summaryFields(len(entries), counts, len(retry), retryFile)...)

	if fileErr != nil {
		return fileErr
	}

	if len(retry) > 0 && retryFile == "" {
		logger.Warn("menu: places to retry were not saved, set -failed to keep them", "retry", len(retry))
	}

	return nil
}
