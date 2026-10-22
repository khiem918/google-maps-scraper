package gmaps

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
	"uuid"

	"github.com/PuerkitoBio/goquery"
	"github.com/gosom/scrapemate"
	"github.com/mxschmitt/playwright-go"

	"github.com/gosom/google-maps-scraper/exiter"
)

const (
	searchResultsTimeout   = 10 * time.Second
	menuPanelTimeout       = 10 * time.Second
	menuPhotoSourceTimeout = 5 * time.Second
	menuPollInterval       = 250 * time.Millisecond

	menuPhotosSourceMetaKey = "menu_photos_source"
)

var errMenuSearchBlocked = errors.New("google search blocked the menu request")

// menuButtonsJS returns the texts of the knowledge-panel async buttons.
const menuButtonsJS = `() => [...document.querySelectorAll('[role=button][data-async-ons]')].map(b => b.innerText || '')`

// menuButtonClickJS clicks the knowledge-panel async button at the given index.
const menuButtonClickJS = `(idx) => {
	const btn = document.querySelectorAll('[role=button][data-async-ons]')[idx];
	if (!btn) return false;
	btn.click();
	return true;
}`

// searchResultsReadyJS reports whether the place knowledge panel of a Google
// Search page has rendered.
const searchResultsReadyJS = `() => !!document.querySelector('[data-feature-id],[role=button][data-async-ons]')`

// menuPanelReadyJS reports whether any part of the menu panel has rendered.
const menuPanelReadyJS = `() => !!document.querySelector('[data-menu-item-id],[role=listitem][data-cid],a[role=listitem] img')`

// menuPhotoClickJS opens the first menu photo, which navigates to its source page.
const menuPhotoClickJS = `() => {
	const img = document.querySelector('a[role=listitem] img');
	if (!img) return false;
	img.click();
	return true;
}`

type MenuJobOptions func(*MenuJob)

// MenuJob looks a place up on Google Search and extracts its menu panel
// (menu sections with prices, popular dishes and menu photos) into Entry.
type MenuJob struct {
	scrapemate.Job

	Entry                   *Entry
	ExtractEmail            bool
	UsageInResults          bool
	Breaker                 *MenuBreaker
	ExitMonitor             exiter.Exiter
	WriterManagedCompletion bool
}

// NewMenuJob creates a job that searches Google for the entry and reads its
// menu panel in langCode.
func NewMenuJob(parentID, langCode string, entry *Entry, opts ...MenuJobOptions) *MenuJob {
	const (
		defaultPrio       = scrapemate.PriorityHigh
		defaultMaxRetries = 0
	)

	job := MenuJob{
		Job: scrapemate.Job{
			ID:       uuid.NewV4().String(),
			ParentID: parentID,
			Method:   requestMethodGet,
			URL:      googleSearchURL,
			URLParams: map[string]string{
				"q":                buildMenuSearchQuery(entry),
				languageQueryParam: langCode,
			},
			MaxRetries: defaultMaxRetries,
			Priority:   defaultPrio,
		},
		Entry:          entry,
		UsageInResults: true,
	}

	for _, opt := range opts {
		opt(&job)
	}

	return &job
}

// WithMenuJobEmail chains an email extraction job after the menu job.
func WithMenuJobEmail() MenuJobOptions {
	return func(j *MenuJob) {
		j.ExtractEmail = true
	}
}

// WithMenuJobBreaker shares a breaker that stops menu extraction once Google
// Search keeps blocking requests.
func WithMenuJobBreaker(b *MenuBreaker) MenuJobOptions {
	return func(j *MenuJob) {
		j.Breaker = b
	}
}

func WithMenuJobExitMonitor(exitMonitor exiter.Exiter) MenuJobOptions {
	return func(j *MenuJob) {
		j.ExitMonitor = exitMonitor
	}
}

func WithMenuJobWriterManagedCompletion() MenuJobOptions {
	return func(j *MenuJob) {
		j.WriterManagedCompletion = true
	}
}

func (j *MenuJob) UseInResults() bool {
	return j.UsageInResults
}

func (j *MenuJob) ProcessOnFetchError() bool {
	return true
}

// Process attaches the parsed menu to the entry. Menu data is best effort: it
// never returns an error, because scrapemate drops the result of a failed job
// and the place itself must still be written.
func (j *MenuJob) Process(ctx context.Context, resp *scrapemate.Response) (any, []scrapemate.IJob, error) {
	defer func() {
		resp.Document = nil
		resp.Body = nil
		resp.Meta = nil
	}()

	j.applyMenu(ctx, resp)

	if j.ExtractEmail && j.Entry.IsWebsiteValidForEmail() {
		opts := []EmailExtractJobOptions{}
		if j.ExitMonitor != nil {
			opts = append(opts, WithEmailJobExitMonitor(j.ExitMonitor))
		}

		if j.WriterManagedCompletion {
			opts = append(opts, WithEmailJobWriterManagedCompletion())
		}

		emailJob := NewEmailJob(j.ID, j.Entry, opts...)

		j.UsageInResults = false

		return nil, []scrapemate.IJob{emailJob}, nil
	}

	if j.ExitMonitor != nil && !j.WriterManagedCompletion {
		j.ExitMonitor.IncrPlacesCompleted(1)
	}

	return j.Entry, nil, nil
}

func (j *MenuJob) applyMenu(ctx context.Context, resp *scrapemate.Response) {
	logger := scrapemate.GetLoggerFromContext(ctx)

	body := string(resp.Body)

	if errors.Is(resp.Error, errMenuSearchBlocked) || isSearchBlocked(resp.URL, body) {
		if j.Breaker.RecordBlocked() {
			logger.Warn("menu extraction disabled: google search blocked repeatedly")
		} else {
			logger.Info("menu: google search blocked", "title", j.Entry.Title)
		}

		return
	}

	if resp.Error != nil {
		logger.Info("menu: fetch failed", "title", j.Entry.Title, "error", resp.Error)

		return
	}

	if !hasKnowledgePanel(body) {
		if j.Breaker.RecordNoPanel() {
			logger.Warn("menu extraction disabled: google search keeps returning results without a knowledge panel (likely rate-limited)")
		} else {
			logger.Info("menu: no knowledge panel in search results", "title", j.Entry.Title)
		}

		return
	}

	j.Breaker.RecordOK()

	if !pageMatchesPlace(body, j.Entry.DataID, j.Entry.Cid) {
		logger.Info("menu: search result does not match place", "title", j.Entry.Title)

		return
	}

	doc, err := goquery.NewDocumentFromReader(bytes.NewReader(resp.Body))
	if err != nil {
		logger.Info("menu: could not parse search page", "title", j.Entry.Title, "error", err)

		return
	}

	menu := parseSearchMenu(doc)

	// The Search panel carries prices, so it supersedes the Maps menu.
	if len(menu.Sections) > 0 {
		j.Entry.MenuItems = menu.Sections
	}

	j.Entry.PopularDishes = menu.PopularDishes
	j.Entry.MenuPhotos = menu.Photos

	if source, ok := resp.Meta[menuPhotosSourceMetaKey].(string); ok {
		j.Entry.MenuPhotos.Source = source
	}
}

func (j *MenuJob) BrowserActions(ctx context.Context, page scrapemate.BrowserPage) scrapemate.Response {
	var resp scrapemate.Response

	restoreUA := overrideSearchUserAgent(page)
	defer restoreUA()

	pageResponse, err := page.Goto(j.GetFullURL(), scrapemate.WaitUntilDOMContentLoaded)

	switch {
	case err == nil:
		resp.StatusCode = pageResponse.StatusCode
		resp.Headers = pageResponse.Headers
	case isGoogleURL(page.URL()):
		// The adapter reads the navigation response body after loading, which
		// fails when Google Search replaces that response right away. The page
		// itself is loaded and is read below with page.Content().
		resp.StatusCode = http.StatusOK
	default:
		resp.Error = err

		return resp
	}

	clickRejectCookiesIfRequired(page)

	// Google Search first serves a JavaScript shell that renders the results
	// (and the knowledge panel) afterwards.
	waitForEval(ctx, page, searchResultsReadyJS, searchResultsTimeout)

	resp.URL = page.URL()
	resp.Meta = map[string]any{}

	if isSearchBlocked(page.URL(), "") {
		resp.URL = page.URL()
		resp.Error = errMenuSearchBlocked

		return resp
	}

	clicked := clickMenuButton(page)
	if clicked {
		waitForEval(ctx, page, menuPanelReadyJS, menuPanelTimeout)
	}

	body, err := page.Content()
	if err != nil {
		resp.Error = err

		return resp
	}

	resp.URL = page.URL()
	resp.Body = []byte(body)

	dumpMenuDebug(j.ID, resp.Body)

	if os.Getenv("GMAPS_DEBUG_DUMP_RAW") == "1" {
		log.Printf("DEBUG: menu page url=%s clicked=%v", resp.URL, clicked)
	}

	if clicked && strings.Contains(body, `role="listitem"`) {
		if source := menuPhotosSource(ctx, page); source != "" {
			resp.Meta[menuPhotosSourceMetaKey] = source
		}
	}

	return resp
}

// clickMenuButton clicks the knowledge-panel button that opens the menu panel.
func clickMenuButton(page scrapemate.BrowserPage) bool {
	textsI, err := page.Eval(menuButtonsJS)
	if err != nil {
		return false
	}

	texts, ok := textsI.([]any)
	if !ok {
		return false
	}

	for i, textI := range texts {
		text, _ := textI.(string)
		if !isMenuButtonLabel(text) {
			continue
		}

		res, err := page.Eval(menuButtonClickJS, i)
		if err != nil {
			return false
		}

		clicked, _ := res.(bool)

		return clicked
	}

	return false
}

// waitForEval polls the boolean JS expression until it is true or timeout
// ends. Evaluation errors (e.g. while the page navigates) are retried.
func waitForEval(ctx context.Context, page scrapemate.BrowserPage, js string, timeout time.Duration) {
	deadline := time.Now().Add(timeout)

	for time.Now().Before(deadline) {
		if res, err := page.Eval(js); err == nil {
			if ready, _ := res.(bool); ready {
				return
			}
		}

		select {
		case <-ctx.Done():
			return
		case <-time.After(menuPollInterval):
		}
	}
}

// menuPhotosSource clicks the first menu photo and returns the non-Google page
// it navigates to, or "" when nothing happens in time.
func menuPhotosSource(ctx context.Context, page scrapemate.BrowserPage) string {
	before := page.URL()

	res, err := page.Eval(menuPhotoClickJS)
	if err != nil {
		return ""
	}

	if clicked, _ := res.(bool); !clicked {
		return ""
	}

	deadline := time.Now().Add(menuPhotoSourceTimeout)

	for time.Now().Before(deadline) {
		if current := page.URL(); current != before {
			if isGoogleURL(current) {
				return ""
			}

			return current
		}

		select {
		case <-ctx.Done():
			return ""
		case <-time.After(menuPollInterval):
		}
	}

	return ""
}

// overrideSearchUserAgent switches the page to a current Chrome user agent
// for the duration of a menu job and returns a func restoring the original.
// scrapemate's default user agent is Chrome 91, which Google Search treats as
// outdated: it answers with a reduced results page without the knowledge
// panel (Google Maps still accepts it, so the place scrape is unaffected).
// Any failure leaves the page untouched.
func overrideSearchUserAgent(page scrapemate.BrowserPage) func() {
	noop := func() {}

	pwPage, ok := page.Unwrap().(playwright.Page)
	if !ok || pwPage == nil {
		return noop
	}

	originalI, err := pwPage.Evaluate("() => navigator.userAgent")
	if err != nil {
		return noop
	}

	original, _ := originalI.(string)

	version := ""
	if browser := pwPage.Context().Browser(); browser != nil {
		version = browser.Version()
	}

	session, err := pwPage.Context().NewCDPSession(pwPage)
	if err != nil {
		return noop
	}

	if _, err := session.Send("Network.setUserAgentOverride", searchUserAgentOverride(version)); err != nil {
		_ = session.Detach()

		return noop
	}

	return func() {
		if original != "" {
			_, _ = session.Send("Network.setUserAgentOverride", map[string]any{"userAgent": original})
		}

		_ = session.Detach()
	}
}

// searchUserAgentOverride builds Network.setUserAgentOverride params for a
// desktop Linux Chrome matching browserVersion (e.g. "141.0.7390.37"), with
// consistent client hints. Falls back to fallbackChromeVersion.
func searchUserAgentOverride(browserVersion string) map[string]any {
	const fallbackChromeVersion = "140.0.0.0"

	if browserVersion == "" || !strings.Contains(browserVersion, ".") {
		browserVersion = fallbackChromeVersion
	}

	major, _, _ := strings.Cut(browserVersion, ".")

	brand := func(name, version string) map[string]any {
		return map[string]any{"brand": name, "version": version}
	}

	return map[string]any{
		"userAgent": fmt.Sprintf(
			"Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/%s.0.0.0 Safari/537.36",
			major,
		),
		"acceptLanguage": "vi-VN,vi;q=0.9,en-US;q=0.8,en;q=0.7",
		"platform":       "Linux x86_64",
		"userAgentMetadata": map[string]any{
			"brands": []map[string]any{
				brand("Chromium", major),
				brand("Google Chrome", major),
				brand("Not=A?Brand", "24"),
			},
			"fullVersion":     browserVersion,
			"platform":        "Linux",
			"platformVersion": "",
			"architecture":    "x86",
			"model":           "",
			"mobile":          false,
		},
	}
}

// isGoogleURL reports whether rawURL points to a Google host.
func isGoogleURL(rawURL string) bool {
	u, err := url.Parse(rawURL)
	if err != nil {
		return false
	}

	host := strings.ToLower(u.Hostname())

	return host == "google.com" || strings.HasPrefix(host, "google.") || strings.Contains(host, ".google.")
}

// dumpMenuDebug writes the Google Search page holding the menu panel to
// /out/menu-<jobID>.html when GMAPS_DEBUG_DUMP_RAW=1, so selectors can be
// fixed quickly when Google changes the panel. No-op otherwise.
func dumpMenuDebug(jobID string, body []byte) {
	if os.Getenv("GMAPS_DEBUG_DUMP_RAW") != "1" {
		return
	}

	path := fmt.Sprintf("/out/menu-%s.html", jobID)
	if err := os.WriteFile(path, body, 0o644); err != nil { //nolint:gosec // debug dump meant to be readable on the host
		log.Printf("DEBUG: failed to write menu dump %s: %v", path, err)
	}
}
