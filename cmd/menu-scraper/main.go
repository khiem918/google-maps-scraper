// Command menu-scraper adds the Google Search menu panel (menu sections with
// prices, popular dishes and menu photos) to places from a google-maps-scraper
// JSON results file, so menus can be collected separately from the Maps crawl.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/gosom/scrapemate"

	"github.com/gosom/google-maps-scraper/exiter"
	"github.com/gosom/google-maps-scraper/gmaps"
	"github.com/gosom/google-maps-scraper/internal/proxyconfig"
	"github.com/gosom/google-maps-scraper/runner"
)

const (
	// menuBlockedThreshold is how many consecutive captcha pages from Google
	// Search stop the run from searching further places.
	menuBlockedThreshold = 3
	// menuNoPanelThreshold is how many consecutive result pages without a
	// knowledge panel (a sign of rate limiting) stop further searches.
	menuNoPanelThreshold = 5
	defaultInactivity    = 3 * time.Minute
)

type config struct {
	input            string
	results          string
	json             bool
	lang             string
	concurrency      int
	proxies          string
	proxiesFile      string
	debug            bool
	disablePageReuse bool
	exitOnInactivity time.Duration
}

func parseFlags() config {
	var cfg config

	flag.StringVar(&cfg.input, "input", "", "google-maps-scraper JSON results file (one place per line), or stdin")
	flag.StringVar(&cfg.results, "results", "stdout", "path to the results file")
	flag.BoolVar(&cfg.json, "json", false, "write results as JSON lines instead of CSV")
	flag.StringVar(&cfg.lang, "lang", "en", "language code for Google Search (e.g. 'vi')")
	flag.IntVar(&cfg.concurrency, "c", 1, "number of places searched in parallel (keep low to avoid Google captchas)")
	flag.StringVar(&cfg.proxies, "proxies", "", "comma separated list of proxies to use (e.g. 'socks5://host:port,http://user:pass@host:port')")
	flag.StringVar(&cfg.proxiesFile, "proxies-file", "", "file with one proxy URL per line")
	flag.BoolVar(&cfg.debug, "debug", false, "show the browser window")
	flag.BoolVar(&cfg.disablePageReuse, "disable-page-reuse", false, "disable page reuse in playwright")
	flag.DurationVar(&cfg.exitOnInactivity, "exit-on-inactivity", defaultInactivity, "exit after inactivity duration (e.g. '5m')")

	flag.Parse()

	return cfg
}

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)

	cfg := parseFlags()

	err := run(ctx, &cfg)

	cancel()

	if err != nil && !errors.Is(err, context.Canceled) {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(ctx context.Context, cfg *config) error {
	if cfg.input == "" {
		return errors.New("-input is required")
	}

	if cfg.concurrency < 1 {
		return errors.New("-c must be greater than 0")
	}

	proxies, err := proxyconfig.Resolve(cfg.proxies, cfg.proxiesFile)
	if err != nil {
		return err
	}

	entries, err := loadEntries(cfg.input)
	if err != nil {
		return err
	}

	exitMonitor := exiter.New()
	breaker := gmaps.NewMenuBreaker(menuBlockedThreshold, menuNoPanelThreshold)

	jobs, skipped := menuJobs(entries, cfg.lang, breaker, exitMonitor)
	if skipped > 0 {
		fmt.Fprintf(os.Stderr, "warning: %d place(s) without a title were skipped\n", skipped)
	}

	if len(jobs) == 0 {
		return errors.New("no places with a title in the input")
	}

	out, closeOut, err := openResults(cfg.results)
	if err != nil {
		return err
	}
	defer closeOut()

	app, err := runner.NewScrapemateApp(&runner.Config{
		Concurrency:              cfg.concurrency,
		ExitOnInactivityDuration: cfg.exitOnInactivity,
		Proxies:                  proxies,
		Debug:                    cfg.debug,
		DisablePageReuse:         cfg.disablePageReuse,
	}, []scrapemate.ResultWriter{runner.NewResultWriter(out, cfg.json)})
	if err != nil {
		return err
	}
	defer app.Close()

	fmt.Fprintf(os.Stderr, "searching menus for %d place(s)\n", len(jobs))

	// Each MenuJob reports itself completed, so the run ends after the last one.
	exitMonitor.IncrPlacesFound(len(jobs))

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	exitMonitor.SetCancelFunc(cancel)

	go exitMonitor.Run(ctx)

	return app.Start(ctx, jobs...)
}

// loadEntries reads the places of a google-maps-scraper JSON results file.
func loadEntries(path string) ([]*gmaps.Entry, error) {
	input, err := runner.OpenInput(path)
	if err != nil {
		return nil, err
	}

	if closer, ok := input.(io.Closer); ok && input != os.Stdin {
		defer closer.Close()
	}

	return readEntries(input)
}

// readEntries decodes consecutive JSON place objects, which is the JSON lines
// format written by google-maps-scraper -json.
func readEntries(r io.Reader) ([]*gmaps.Entry, error) {
	var entries []*gmaps.Entry

	dec := json.NewDecoder(r)

	for {
		var entry gmaps.Entry

		err := dec.Decode(&entry)
		if errors.Is(err, io.EOF) {
			return entries, nil
		}

		if err != nil {
			return nil, fmt.Errorf("place %d: %w (the input must be a -json results file)", len(entries)+1, err)
		}

		entries = append(entries, &entry)
	}
}

// menuJobs creates one MenuJob per entry that has a title to search for and
// returns how many entries were skipped.
func menuJobs(entries []*gmaps.Entry, lang string, breaker *gmaps.MenuBreaker, exitMonitor exiter.Exiter) (jobs []scrapemate.IJob, skipped int) {
	jobs = make([]scrapemate.IJob, 0, len(entries))

	for _, entry := range entries {
		if entry.Title == "" {
			skipped++

			continue
		}

		jobs = append(jobs, gmaps.NewMenuJob("", lang, entry,
			gmaps.WithMenuJobBreaker(breaker),
			gmaps.WithMenuJobExitMonitor(exitMonitor),
		))
	}

	return jobs, skipped
}

func openResults(path string) (io.Writer, func(), error) {
	if path == "stdout" {
		return os.Stdout, func() {}, nil
	}

	f, err := os.Create(path)
	if err != nil {
		return nil, nil, err
	}

	return f, func() { _ = f.Close() }, nil
}
