package runner

import (
	"encoding/csv"
	"io"
	"os"

	"github.com/gosom/scrapemate"
	"github.com/gosom/scrapemate/adapters/writers/csvwriter"
	"github.com/gosom/scrapemate/adapters/writers/jsonwriter"
	"github.com/gosom/scrapemate/scrapemateapp"
)

// OpenInput opens the input file, or returns os.Stdin when path is "stdin".
func OpenInput(path string) (io.Reader, error) {
	if path == "stdin" {
		return os.Stdin, nil
	}

	return os.Open(path)
}

// NewResultWriter returns a JSON lines writer when jsonOutput is set, and a
// CSV writer otherwise.
func NewResultWriter(w io.Writer, jsonOutput bool) scrapemate.ResultWriter {
	if jsonOutput {
		return jsonwriter.NewJSONWriter(w)
	}

	return csvwriter.NewCsvWriter(csv.NewWriter(w))
}

// ScrapemateOptions returns the scrapemate app options for a file based run:
// concurrency, proxies, the browser (or stealth HTTP client in fast mode) and
// page/browser reuse limits.
func ScrapemateOptions(cfg *Config) []func(*scrapemateapp.Config) error {
	opts := []func(*scrapemateapp.Config) error{
		// scrapemateapp.WithCache("leveldb", "cache"),
		scrapemateapp.WithConcurrency(cfg.Concurrency),
		scrapemateapp.WithExitOnInactivity(cfg.ExitOnInactivityDuration),
	}

	if len(cfg.Proxies) > 0 {
		opts = append(opts,
			scrapemateapp.WithProxies(cfg.Proxies),
		)
	}

	if !cfg.FastMode {
		if cfg.Debug {
			opts = append(opts, scrapemateapp.WithJS(
				scrapemateapp.Headfull(),
				scrapemateapp.DisableImages(),
			))
		} else {
			opts = append(opts, scrapemateapp.WithJS(scrapemateapp.DisableImages()))
		}
	} else {
		opts = append(opts, scrapemateapp.WithStealth("firefox"))
	}

	opts = AppendBrowserCapacityOptions(opts, cfg)

	if !cfg.DisablePageReuse {
		opts = append(opts,
			scrapemateapp.WithPageReuseLimit(2),
			scrapemateapp.WithBrowserReuseLimit(200),
		)
	}

	return opts
}

// NewScrapemateApp builds a scrapemate app from ScrapemateOptions that writes
// its results to writers.
func NewScrapemateApp(cfg *Config, writers []scrapemate.ResultWriter) (*scrapemateapp.ScrapemateApp, error) {
	matecfg, err := scrapemateapp.NewConfig(
		writers,
		ScrapemateOptions(cfg)...,
	)
	if err != nil {
		return nil, err
	}

	return scrapemateapp.NewScrapeMateApp(matecfg)
}
