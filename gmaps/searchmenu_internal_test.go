package gmaps

import (
	"bytes"
	"context"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/PuerkitoBio/goquery"
	"github.com/gosom/scrapemate"
	"github.com/stretchr/testify/require"
)

const (
	busanDataID = "0x31752f6efee842a7:0x952ff60ca5d0b766"
	busanCid    = "10750081369738622822"
)

func loadSearchMenuFixture(t *testing.T) []byte {
	t.Helper()

	raw, err := os.ReadFile("testdata/search_menu_busan.html")
	require.NoError(t, err)

	return raw
}

func searchMenuDoc(t *testing.T, raw []byte) *goquery.Document {
	t.Helper()

	doc, err := goquery.NewDocumentFromReader(bytes.NewReader(raw))
	require.NoError(t, err)

	return doc
}

func TestParseSearchMenuSections(t *testing.T) {
	sections := parseSearchMenu(searchMenuDoc(t, loadSearchMenuFixture(t))).Sections

	names := make([]string, 0, len(sections))
	for _, s := range sections {
		names = append(names, s.Name)
	}

	require.Equal(t, []string{"At a Glance", "Topping", "Appitizer (간식)", "Drink"}, names)

	require.Equal(t, []MenuItem{
		{Name: "Cheese", Price: "18.000 ₫", Description: "(Phô mai)"},
		{Name: "Boiled Egg", Price: "5.000 ₫", Description: "(Trứng)"},
	}, sections[1].Items)

	require.Equal(t, MenuItem{
		Name:        "Bánh Xèo Hải Sản",
		Price:       "75.000 ₫",
		Description: "베트남식 해산물 빈대떡 (pancake with seafood, haemul pajeon)",
	}, sections[2].Items[0])

	require.Equal(t, []MenuItem{
		{Name: "Siro Blue"},
		{Name: "Mirinda Lon", Description: "(Cam, xá xị)"},
	}, sections[3].Items)
}

func TestParsePopularDishes(t *testing.T) {
	dishes := parseSearchMenu(searchMenuDoc(t, loadSearchMenuFixture(t))).PopularDishes

	require.Equal(t, []PopularDish{
		{
			Name:     "Cơm Trộn Thịt Bò",
			Label:    "Được ưa chuộng",
			Image:    "https://lh3.googleusercontent.com/grass-cs/AAA=s202-w152-h202-p-k-no",
			EntityID: "/g/11h0dh0tgv",
		},
		{
			Name:     "Mì Tương Đen",
			Label:    "Được ưa chuộng",
			Image:    "https://lh3.googleusercontent.com/grass-cs/BBB=s202-w152-h202-p-k-no",
			EntityID: "/g/11bc6x1x1x",
		},
		{
			Name:     "Udon Bò",
			EntityID: "/g/11ccccc",
		},
	}, dishes)
}

func TestParseMenuPhotos(t *testing.T) {
	photos := parseSearchMenu(searchMenuDoc(t, loadSearchMenuFixture(t))).Photos

	require.Equal(t, MenuPhotos{
		Count:  3,
		Images: []string{"https://lh3.googleusercontent.com/p/MENU3=w800"},
	}, photos)
}

func TestParseSearchMenuEmpty(t *testing.T) {
	menu := parseSearchMenu(searchMenuDoc(t, []byte(`<html><body><div>no menu</div></body></html>`)))

	require.True(t, menu.IsEmpty())
	require.Nil(t, menu.Sections)
	require.Nil(t, menu.PopularDishes)
}

func TestPageMatchesPlace(t *testing.T) {
	html := string(loadSearchMenuFixture(t))

	require.True(t, pageMatchesPlace(html, busanDataID, ""))
	require.True(t, pageMatchesPlace(html, "", busanCid))
	require.True(t, pageMatchesPlace(`"ludocid=`+busanCid+`"`, "", busanCid))
	require.False(t, pageMatchesPlace(html, "0x1:0x2", "123"))
	require.False(t, pageMatchesPlace(html, "", "1075008136973862282"), "cid prefix must not match")
	require.True(t, pageMatchesPlace("ludocid=1&ludocid="+busanCid, "", busanCid))
	require.False(t, pageMatchesPlace(html, "", ""))
}

func TestIsSearchBlocked(t *testing.T) {
	require.True(t, isSearchBlocked("https://www.google.com/sorry/index?continue=x", ""))
	require.True(t, isSearchBlocked("https://www.google.com/search?q=x", `<form id="captcha-form">`))
	require.False(t, isSearchBlocked("https://www.google.com/search?q=x", string(loadSearchMenuFixture(t))))
}

func TestIsMenuButtonLabel(t *testing.T) {
	require.True(t, isMenuButtonLabel("Menu"))
	require.True(t, isMenuButtonLabel(" thực đơn "))
	require.False(t, isMenuButtonLabel("Đặt qua mạng"))
}

func TestBuildMenuSearchQuery(t *testing.T) {
	require.Equal(t, "Busan 4 Đ. Đồng Nai", buildMenuSearchQuery(&Entry{Title: "Busan", Address: "4 Đ. Đồng Nai"}))
	require.Equal(t, "Busan", buildMenuSearchQuery(&Entry{Title: "Busan"}))
	require.Empty(t, buildMenuSearchQuery(&Entry{Address: "4 Đ. Đồng Nai"}))
	require.Empty(t, buildMenuSearchQuery(nil))
}

func TestMenuBreaker(t *testing.T) {
	b := NewMenuBreaker(3, 5)

	require.False(t, b.RecordBlocked())
	require.False(t, b.RecordBlocked())
	b.RecordOK()
	require.True(t, b.Allow())

	require.False(t, b.RecordBlocked())
	require.False(t, b.RecordBlocked())
	require.True(t, b.RecordBlocked())
	require.False(t, b.Allow())
	require.False(t, b.RecordBlocked())

	var nilBreaker *MenuBreaker

	require.True(t, nilBreaker.Allow())
	require.False(t, nilBreaker.RecordBlocked())
	nilBreaker.RecordOK()
}

func TestMenuBreakerConcurrent(t *testing.T) {
	const workers = 50

	b := NewMenuBreaker(3, 5)

	var (
		wg    sync.WaitGroup
		trips atomic.Int32
	)

	for range workers {
		wg.Add(1)

		go func() {
			defer wg.Done()

			if b.RecordBlocked() {
				trips.Add(1)
			}
		}()
	}

	wg.Wait()

	require.Equal(t, int32(1), trips.Load())
	require.False(t, b.Allow())
}

func TestNewMenuJob(t *testing.T) {
	entry := &Entry{Title: "Busan Korean Food", Address: "4 Đ. Đồng Nai"}
	job := NewMenuJob("parent", "vi", entry)

	require.Equal(t, "parent", job.ParentID)
	require.True(t, job.UseInResults())
	require.True(t, job.ProcessOnFetchError())
	require.True(t, strings.HasPrefix(job.GetFullURL(), googleSearchURL+"?"))
	require.Contains(t, job.GetFullURL(), "hl=vi")
	require.Contains(t, job.GetFullURL(), "q=Busan+Korean+Food")
}

func TestMenuJobProcessNeverErrors(t *testing.T) {
	raw := loadSearchMenuFixture(t)

	tests := []struct {
		name         string
		entry        Entry
		resp         scrapemate.Response
		wantSections int
		wantPhotos   int
	}{
		{
			name:  "blocked",
			entry: Entry{Title: "Busan", DataID: busanDataID},
			resp:  scrapemate.Response{Error: errMenuSearchBlocked},
		},
		{
			name:  "other place",
			entry: Entry{Title: "Busan", DataID: "0x1:0x2", Cid: "1"},
			resp:  scrapemate.Response{URL: "https://www.google.com/search?q=x", Body: raw},
		},
		{
			name:         "matching place",
			entry:        Entry{Title: "Busan", DataID: busanDataID},
			resp:         scrapemate.Response{URL: "https://www.google.com/search?q=x", Body: raw},
			wantSections: 4,
			wantPhotos:   3,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			entry := tc.entry
			job := NewMenuJob("parent", "vi", &entry, WithMenuJobBreaker(NewMenuBreaker(3, 5)))
			resp := tc.resp

			got, next, err := job.Process(context.Background(), &resp)

			require.NoError(t, err)
			require.Empty(t, next)
			require.Same(t, &entry, got)
			require.Len(t, entry.MenuItems, tc.wantSections)
			require.Equal(t, tc.wantPhotos, entry.MenuPhotos.Count)
			require.True(t, job.UseInResults())
		})
	}
}

func TestMenuJobKeepsMapsMenuWhenSearchHasNone(t *testing.T) {
	mapsMenu := []MenuSection{{Name: "Maps", Items: []MenuItem{{Name: "Pho"}}}}
	entry := Entry{Title: "Busan", DataID: busanDataID, MenuItems: mapsMenu}
	job := NewMenuJob("parent", "vi", &entry)
	resp := scrapemate.Response{
		URL:  "https://www.google.com/search?q=x",
		Body: []byte(`<div data-feature-id="` + busanDataID + `"></div>`),
	}

	_, _, err := job.Process(context.Background(), &resp)

	require.NoError(t, err)
	require.Equal(t, mapsMenu, entry.MenuItems)
}

func TestMenuJobPhotosSourceFromMeta(t *testing.T) {
	entry := Entry{Title: "Busan", DataID: busanDataID}
	job := NewMenuJob("parent", "vi", &entry)
	resp := scrapemate.Response{
		URL:  "https://www.google.com/search?q=x",
		Body: loadSearchMenuFixture(t),
		Meta: map[string]any{menuPhotosSourceMetaKey: "https://www.foody.vn/x"},
	}

	_, _, err := job.Process(context.Background(), &resp)

	require.NoError(t, err)
	require.Equal(t, "https://www.foody.vn/x", entry.MenuPhotos.Source)
}

func TestMenuJobChainsEmail(t *testing.T) {
	entry := Entry{Title: "Busan", WebSite: "https://example.com"}
	job := NewMenuJob("parent", "vi", &entry, WithMenuJobEmail())
	resp := scrapemate.Response{Error: errMenuSearchBlocked}

	got, next, err := job.Process(context.Background(), &resp)

	require.NoError(t, err)
	require.Nil(t, got)
	require.Len(t, next, 1)
	require.IsType(t, &EmailExtractJob{}, next[0])
	require.False(t, job.UseInResults())
}

func TestIsGoogleURL(t *testing.T) {
	require.True(t, isGoogleURL("https://www.google.com/search?q=x"))
	require.True(t, isGoogleURL("https://google.com.vn/search"))
	require.True(t, isGoogleURL("https://www.google.de/"))
	require.False(t, isGoogleURL("https://www.foody.vn/ho-chi-minh/busan"))
	require.False(t, isGoogleURL("about:blank"))
	require.False(t, isGoogleURL("https://notgoogle.com/"))
}

// degradedSearchPage mimics the results page Google serves to rate-limited
// sessions: organic results but no place knowledge panel.
const degradedSearchPage = `<html><body><div id="rso"><a href="https://www.foody.vn/x">Busan</a></div></body></html>`

func TestHasKnowledgePanel(t *testing.T) {
	require.True(t, hasKnowledgePanel(string(loadSearchMenuFixture(t))))
	require.False(t, hasKnowledgePanel(degradedSearchPage))
	require.False(t, hasKnowledgePanel(""))
}

func TestMenuBreakerNoPanel(t *testing.T) {
	b := NewMenuBreaker(3, 5)

	for range 4 {
		require.False(t, b.RecordNoPanel())
	}

	b.RecordOK()

	for range 4 {
		require.False(t, b.RecordNoPanel())
	}

	require.True(t, b.Allow(), "RecordOK must reset the no-panel streak")
	require.True(t, b.RecordNoPanel())
	require.False(t, b.Allow())
	require.False(t, b.RecordNoPanel(), "only the tripping call returns true")
}

func TestMenuBreakerStreaksAreIndependent(t *testing.T) {
	b := NewMenuBreaker(3, 5)

	require.False(t, b.RecordBlocked())
	require.False(t, b.RecordNoPanel())
	require.False(t, b.RecordBlocked())
	require.False(t, b.RecordNoPanel())
	require.True(t, b.RecordBlocked(), "captcha streak trips at its own threshold")
	require.False(t, b.Allow())

	var nilBreaker *MenuBreaker

	require.False(t, nilBreaker.RecordNoPanel())
}

func TestMenuJobDegradedPageTripsBreaker(t *testing.T) {
	breaker := NewMenuBreaker(3, 2)

	for range 2 {
		entry := Entry{Title: "Busan", DataID: busanDataID}
		job := NewMenuJob("parent", "vi", &entry, WithMenuJobBreaker(breaker))
		resp := scrapemate.Response{URL: "https://www.google.com/search?q=x", Body: []byte(degradedSearchPage)}

		got, next, err := job.Process(context.Background(), &resp)

		require.NoError(t, err)
		require.Empty(t, next)
		require.Same(t, &entry, got)
		require.Nil(t, entry.MenuItems)
	}

	require.False(t, breaker.Allow())
}

func TestMenuJobDegradedPageDoesNotResetBlockedStreak(t *testing.T) {
	breaker := NewMenuBreaker(2, 10)

	require.False(t, breaker.RecordBlocked())

	entry := Entry{Title: "Busan", DataID: busanDataID}
	job := NewMenuJob("parent", "vi", &entry, WithMenuJobBreaker(breaker))
	resp := scrapemate.Response{URL: "https://www.google.com/search?q=x", Body: []byte(degradedSearchPage)}

	_, _, err := job.Process(context.Background(), &resp)
	require.NoError(t, err)

	require.True(t, breaker.RecordBlocked(), "a degraded page must not reset the captcha streak")
}

func TestMenuJobKnowledgePanelResetsStreaks(t *testing.T) {
	breaker := NewMenuBreaker(2, 2)

	require.False(t, breaker.RecordBlocked())
	require.False(t, breaker.RecordNoPanel())

	entry := Entry{Title: "Busan", DataID: busanDataID}
	job := NewMenuJob("parent", "vi", &entry, WithMenuJobBreaker(breaker))
	resp := scrapemate.Response{URL: "https://www.google.com/search?q=x", Body: loadSearchMenuFixture(t)}

	_, _, err := job.Process(context.Background(), &resp)
	require.NoError(t, err)

	require.False(t, breaker.RecordBlocked())
	require.False(t, breaker.RecordNoPanel())
	require.True(t, breaker.Allow())
}

func TestSearchUserAgentOverride(t *testing.T) {
	params := searchUserAgentOverride("141.0.7390.37")

	require.Equal(t,
		"Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/141.0.0.0 Safari/537.36",
		params["userAgent"])

	meta, ok := params["userAgentMetadata"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "141.0.7390.37", meta["fullVersion"])
	require.Equal(t, "Linux", meta["platform"])

	fallback := searchUserAgentOverride("")
	require.Contains(t, fallback["userAgent"], "Chrome/140.0.0.0")
	require.NotContains(t, fallback["userAgent"], "Chrome/91")
}
