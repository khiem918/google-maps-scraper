package runner_test

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gosom/scrapemate"
	"github.com/gosom/scrapemate/scrapemateapp"

	"github.com/gosom/google-maps-scraper/gmaps"
	"github.com/gosom/google-maps-scraper/runner"
)

func newMateConfig(t *testing.T, cfg *runner.Config) *scrapemateapp.Config {
	t.Helper()

	writers := []scrapemate.ResultWriter{runner.NewResultWriter(&bytes.Buffer{}, true)}

	matecfg, err := scrapemateapp.NewConfig(writers, runner.ScrapemateOptions(cfg)...)
	if err != nil {
		t.Fatalf("NewConfig: %v", err)
	}

	return matecfg
}

func TestScrapemateOptionsDefault(t *testing.T) {
	t.Parallel()

	matecfg := newMateConfig(t, &runner.Config{
		Concurrency:              3,
		ExitOnInactivityDuration: 5 * time.Minute,
	})

	if matecfg.Concurrency != 3 {
		t.Errorf("Concurrency = %d, want 3", matecfg.Concurrency)
	}

	if matecfg.ExitOnInactivityDuration != 5*time.Minute {
		t.Errorf("ExitOnInactivityDuration = %v, want 5m", matecfg.ExitOnInactivityDuration)
	}

	if !matecfg.UseJS || matecfg.UseStealth {
		t.Errorf("UseJS = %v, UseStealth = %v, want browser mode", matecfg.UseJS, matecfg.UseStealth)
	}

	if matecfg.PageReuseLimit != 2 || matecfg.BrowserReuseLimit != 200 {
		t.Errorf("reuse limits = %d/%d, want 2/200", matecfg.PageReuseLimit, matecfg.BrowserReuseLimit)
	}

	if len(matecfg.Proxies) != 0 {
		t.Errorf("Proxies = %v, want none", matecfg.Proxies)
	}

	if matecfg.MaxPagesPerBrowser != 1 || matecfg.BrowserPoolSize != 0 {
		t.Errorf("capacity = %d pages/%d browsers, want scrapemate defaults", matecfg.MaxPagesPerBrowser, matecfg.BrowserPoolSize)
	}
}

func TestScrapemateOptionsFastModeProxiesCapacityNoReuse(t *testing.T) {
	t.Parallel()

	proxies := []string{"http://127.0.0.1:8080"}

	matecfg := newMateConfig(t, &runner.Config{
		Concurrency:        1,
		FastMode:           true,
		Proxies:            proxies,
		MaxPagesPerBrowser: 4,
		BrowserPoolSize:    2,
		DisablePageReuse:   true,
	})

	if matecfg.UseJS || !matecfg.UseStealth || matecfg.StealthBrowser != "firefox" {
		t.Errorf("UseJS = %v, UseStealth = %v, StealthBrowser = %q, want firefox stealth", matecfg.UseJS, matecfg.UseStealth, matecfg.StealthBrowser)
	}

	if len(matecfg.Proxies) != 1 || matecfg.Proxies[0] != proxies[0] {
		t.Errorf("Proxies = %v, want %v", matecfg.Proxies, proxies)
	}

	if matecfg.MaxPagesPerBrowser != 4 || matecfg.BrowserPoolSize != 2 {
		t.Errorf("capacity = %d pages/%d browsers, want 4/2", matecfg.MaxPagesPerBrowser, matecfg.BrowserPoolSize)
	}

	if matecfg.PageReuseLimit != 0 || matecfg.BrowserReuseLimit != 0 {
		t.Errorf("reuse limits = %d/%d, want disabled", matecfg.PageReuseLimit, matecfg.BrowserReuseLimit)
	}
}

func TestNewResultWriterFormats(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name       string
		jsonOutput bool
		want       string
	}{
		{name: "json", jsonOutput: true, want: `"title":"Busan Korean Food"`},
		{name: "csv", jsonOutput: false, want: "input_id,link,title"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			var buf bytes.Buffer

			results := make(chan scrapemate.Result, 1)
			results <- scrapemate.Result{Data: &gmaps.Entry{Title: "Busan Korean Food", Cid: "1"}}

			close(results)

			if err := runner.NewResultWriter(&buf, tc.jsonOutput).Run(context.Background(), results); err != nil {
				t.Fatalf("Run: %v", err)
			}

			if !strings.Contains(buf.String(), tc.want) {
				t.Errorf("output %q does not contain %q", buf.String(), tc.want)
			}
		})
	}
}

func TestOpenInput(t *testing.T) {
	t.Parallel()

	input, err := runner.OpenInput("stdin")
	if err != nil || input != os.Stdin {
		t.Fatalf("OpenInput(stdin) = %v, %v; want os.Stdin", input, err)
	}

	path := filepath.Join(t.TempDir(), "queries.txt")
	if err := os.WriteFile(path, []byte("cafe\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	input, err = runner.OpenInput(path)
	if err != nil {
		t.Fatalf("OpenInput(file): %v", err)
	}

	f, ok := input.(*os.File)
	if !ok {
		t.Fatalf("OpenInput(file) = %T, want *os.File", input)
	}

	_ = f.Close()

	if _, err := runner.OpenInput(filepath.Join(t.TempDir(), "missing.txt")); err == nil {
		t.Error("OpenInput(missing) returned no error")
	}
}
