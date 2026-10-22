package gmaps

import (
	"strings"
	"sync/atomic"

	"github.com/PuerkitoBio/goquery"
)

const googleSearchURL = "https://www.google.com/search"

// menuButtonLabels are the knowledge-panel button texts that open the menu
// panel, compared case-insensitively after trimming.
var menuButtonLabels = []string{"menu", "thực đơn", "menü", "carta", "cardápio", "メニュー", "메뉴", "菜单", "菜單"}

// SearchMenu is the data parsed from the Google Search menu panel.
type SearchMenu struct {
	Sections      []MenuSection
	PopularDishes []PopularDish
	Photos        MenuPhotos
}

// IsEmpty reports whether nothing was parsed from the menu panel.
func (m *SearchMenu) IsEmpty() bool {
	return len(m.Sections) == 0 && len(m.PopularDishes) == 0 && m.Photos.Count == 0
}

// buildMenuSearchQuery returns the Google Search query used to surface the
// knowledge panel of a place, or "" when the entry lacks a title.
func buildMenuSearchQuery(entry *Entry) string {
	if entry == nil || strings.TrimSpace(entry.Title) == "" {
		return ""
	}

	return strings.TrimSpace(entry.Title + " " + entry.Address)
}

// isMenuButtonLabel reports whether text is one of menuButtonLabels.
func isMenuButtonLabel(text string) bool {
	text = cleanMenuText(text)

	for _, label := range menuButtonLabels {
		if strings.EqualFold(text, label) {
			return true
		}
	}

	return false
}

// pageMatchesPlace reports whether the search result page belongs to the
// place, using the knowledge panel feature id (data_id) or ludocid (cid).
func pageMatchesPlace(html, dataID, cid string) bool {
	if dataID != "" && strings.Contains(html, `data-feature-id="`+dataID+`"`) {
		return true
	}

	if cid != "" && (containsNumber(html, "ludocid="+cid) || containsNumber(html, `ludocid=`+cid)) {
		return true
	}

	return false
}

// containsNumber reports whether s contains needle, which ends in a number,
// without further digits right after it (so cid 1 does not match cid 10).
func containsNumber(s, needle string) bool {
	for {
		idx := strings.Index(s, needle)
		if idx < 0 {
			return false
		}

		end := idx + len(needle)
		if end == len(s) || s[end] < '0' || s[end] > '9' {
			return true
		}

		s = s[end:]
	}
}

// isSearchBlocked reports whether Google answered with a captcha / unusual
// traffic page instead of results.
func isSearchBlocked(pageURL, html string) bool {
	return strings.Contains(pageURL, "/sorry/") ||
		strings.Contains(html, `id="captcha-form"`) ||
		strings.Contains(html, "unusual traffic")
}

// parseSearchMenu extracts the menu panel from a Google Search result page.
func parseSearchMenu(doc *goquery.Document) SearchMenu {
	return SearchMenu{
		Sections:      parseMenuSections(doc),
		PopularDishes: parsePopularDishes(doc),
		Photos:        parseMenuPhotos(doc),
	}
}

// parseMenuSections returns one MenuSection per menu tab. Each tab is a
// role=group labelled by its heading and holding data-menu-item-id items.
func parseMenuSections(doc *goquery.Document) []MenuSection {
	var sections []MenuSection

	doc.Find("[role=group][aria-labelledby]").Each(func(_ int, group *goquery.Selection) {
		items := group.Find("[data-menu-item-id]")
		if items.Length() == 0 {
			return
		}

		id := group.AttrOr("aria-labelledby", "")
		section := MenuSection{
			Name: cleanMenuText(group.Find("[id='" + id + "']").First().Text()),
		}

		items.Each(func(_ int, item *goquery.Selection) {
			if menuItem, ok := parseMenuItem(item); ok {
				section.Items = append(section.Items, menuItem)
			}
		})

		if len(section.Items) == 0 {
			return
		}

		sections = append(sections, section)
	})

	return sections
}

// parseMenuItem reads an item laid out as
// <item><content><row><name/><price/></row><description/></content></item>.
func parseMenuItem(item *goquery.Selection) (MenuItem, bool) {
	content := item.Children().First()
	row := content.Children().First()

	name := cleanMenuText(row.Children().First().Text())
	if name == "" {
		return MenuItem{}, false
	}

	return MenuItem{
		Name:        name,
		Price:       cleanMenuText(row.Children().Eq(1).Text()),
		Description: cleanMenuText(content.Children().Eq(1).Text()),
	}, true
}

// parsePopularDishes reads the "popular dishes" carousel, whose entries carry
// the dish entity id in data-cid.
func parsePopularDishes(doc *goquery.Document) []PopularDish {
	var dishes []PopularDish

	seen := map[string]bool{}

	doc.Find("[role=listitem][data-cid]").Each(func(_ int, s *goquery.Selection) {
		wrap := s.Children().First()

		dish := PopularDish{
			Name:     cleanMenuText(wrap.Children().Eq(1).Text()),
			Label:    cleanMenuText(wrap.Children().Eq(2).Text()),
			Image:    imageURL(s.Find("img").First()),
			EntityID: s.AttrOr("data-cid", ""),
		}

		if dish.Name == "" {
			return
		}

		key := dish.EntityID + "|" + dish.Name
		if seen[key] {
			return
		}

		seen[key] = true

		dishes = append(dishes, dish)
	})

	return dishes
}

// parseMenuPhotos reads the menu photo carousel. Its slides are anchors, which
// distinguishes them from the popular dishes carousel.
func parseMenuPhotos(doc *goquery.Document) MenuPhotos {
	var photos MenuPhotos

	doc.Find("a[role=listitem] img").Each(func(_ int, img *goquery.Selection) {
		photos.Count++

		if u := imageURL(img); u != "" {
			photos.Images = append(photos.Images, u)
		}
	})

	return photos
}

// imageURL returns the http(s) URL of an image, preferring src over the
// lazy-loaded data-src, or "" when only inline data is present.
func imageURL(img *goquery.Selection) string {
	for _, attr := range []string{"src", "data-src"} {
		if v := img.AttrOr(attr, ""); strings.HasPrefix(v, "http") {
			return v
		}
	}

	return ""
}

// cleanMenuText replaces non-breaking spaces and collapses whitespace.
func cleanMenuText(s string) string {
	return strings.Join(strings.Fields(strings.ReplaceAll(s, " ", " ")), " ")
}

// hasKnowledgePanel reports whether a Google Search page shows a place
// knowledge panel. Rate-limited sessions get a degraded results page without
// it, which is not a captcha and must be detected separately.
func hasKnowledgePanel(html string) bool {
	return strings.Contains(html, `data-feature-id="`)
}

// MenuBreaker disables menu extraction for the rest of a run when Google
// Search keeps refusing it: either captcha pages (blocked) or degraded result
// pages without a knowledge panel (no panel), each counted as a consecutive
// streak with its own threshold. A nil *MenuBreaker always allows.
type MenuBreaker struct {
	blockedThreshold int32
	noPanelThreshold int32
	blockedStreak    atomic.Int32
	noPanelStreak    atomic.Int32
	tripped          atomic.Bool
}

// NewMenuBreaker returns a breaker that trips after blockedThreshold
// consecutive captcha pages or noPanelThreshold consecutive pages without a
// knowledge panel. Thresholds below 1 are treated as 1.
func NewMenuBreaker(blockedThreshold, noPanelThreshold int) *MenuBreaker {
	return &MenuBreaker{
		blockedThreshold: clampThreshold(blockedThreshold),
		noPanelThreshold: clampThreshold(noPanelThreshold),
	}
}

func clampThreshold(threshold int) int32 {
	const maxThreshold = 1 << 30

	return int32(min(max(threshold, 1), maxThreshold))
}

// Allow reports whether menu extraction is still enabled.
func (b *MenuBreaker) Allow() bool {
	return b == nil || !b.tripped.Load()
}

// RecordOK resets both streaks. Call it only when a knowledge panel was seen.
func (b *MenuBreaker) RecordOK() {
	if b != nil {
		b.blockedStreak.Store(0)
		b.noPanelStreak.Store(0)
	}
}

// RecordBlocked counts a captcha page and returns true only on the call that
// trips the breaker.
func (b *MenuBreaker) RecordBlocked() bool {
	if b == nil {
		return false
	}

	return b.record(&b.blockedStreak, b.blockedThreshold)
}

// RecordNoPanel counts a results page without a knowledge panel and returns
// true only on the call that trips the breaker.
func (b *MenuBreaker) RecordNoPanel() bool {
	if b == nil {
		return false
	}

	return b.record(&b.noPanelStreak, b.noPanelThreshold)
}

func (b *MenuBreaker) record(streak *atomic.Int32, threshold int32) bool {
	if streak.Add(1) >= threshold {
		return b.tripped.CompareAndSwap(false, true)
	}

	return false
}
