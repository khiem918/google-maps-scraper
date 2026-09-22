package gmaps

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestParseReviewsRelativeDatePaths(t *testing.T) {
	tests := []struct {
		name    string
		wrapped bool
		path    []int
		when    string
	}{
		{
			name:    "inline review metadata path",
			wrapped: true,
			path:    []int{1, 6},
			when:    "7 months ago",
		},
		{
			name: "direct review fallback path",
			path: []int{3, 3},
			when: "3 weeks ago",
		},
		{
			name: "nested review rpc path",
			path: []int{2, 1, 3, 8, 0},
			when: "4 years ago",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			review := newReviewElement()
			setNested(review, tt.when, tt.path...)

			item := any(review)
			if tt.wrapped {
				item = []any{review}
			}

			reviews := parseReviews([]any{item})

			require.Len(t, reviews, 1)
			require.Equal(t, tt.when, reviews[0].When)
			require.Equal(t, "Ada Lovelace", reviews[0].Name)
			require.Equal(t, 5, reviews[0].Rating)
			require.Equal(t, "Clear review text", reviews[0].Description)
		})
	}
}

func TestParseReviewsPublishedAtFromMicrosecondTimestamp(t *testing.T) {
	review := newReviewElement()
	setNested(review, "6 months ago", 1, 6)
	setNested(review, 1764184138462529.0, 1, 2)

	reviews := parseReviews([]any{review})

	require.Len(t, reviews, 1)
	require.NotNil(t, reviews[0].PublishedAt)
	require.Equal(t, "2025-11-26T19:08:58.462529Z", reviews[0].PublishedAt.Format(time.RFC3339Nano))
}

func TestParseReviewsPublishedAtFallsBackToSecondTimestampPath(t *testing.T) {
	review := newReviewElement()
	setNested(review, "6 months ago", 1, 6)
	setNested(review, 1764184138462529.0, 1, 3)

	reviews := parseReviews([]any{review})

	require.Len(t, reviews, 1)
	require.NotNil(t, reviews[0].PublishedAt)
	require.Equal(t, "2025-11-26T19:08:58.462529Z", reviews[0].PublishedAt.Format(time.RFC3339Nano))
}

func TestParseReviewsPublishedAtRejectsInvalidTimestamps(t *testing.T) {
	tests := []struct {
		name   string
		micros float64
	}{
		{
			name: "missing",
		},
		{
			name:   "before lower bound",
			micros: float64(time.Date(2006, time.December, 31, 23, 59, 59, 0, time.UTC).UnixMicro()),
		},
		{
			name:   "too far in future",
			micros: float64(time.Now().UTC().Add(48 * time.Hour).UnixMicro()),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			review := newReviewElement()
			setNested(review, "6 months ago", 1, 6)

			if tt.micros != 0 {
				setNested(review, tt.micros, 1, 2)
			}

			reviews := parseReviews([]any{review})

			require.Len(t, reviews, 1)
			require.Nil(t, reviews[0].PublishedAt)
		})
	}
}

func TestParseReviewsSkipsItemsWithoutAuthor(t *testing.T) {
	review := make([]any, 4)
	setNested(review, "yesterday", 1, 6)
	setNested(review, 5.0, 2, 0, 0)

	reviews := parseReviews([]any{review})

	require.Empty(t, reviews)
}

func newReviewElement() []any {
	review := make([]any, 4)

	setNested(review, "Ada Lovelace", 1, 4, 5, 0)
	setNested(review, 5.0, 2, 0, 0)
	setNested(review, "Clear review text", 2, 15, 0, 0)

	return review
}

func setNested(root []any, value any, path ...int) {
	setNestedValue(root, value, path...)
}

func setNestedValue(current []any, value any, path ...int) []any {
	if len(path) == 0 {
		return current
	}

	index := path[0]
	current = ensureLen(current, index+1)

	if len(path) == 1 {
		current[index] = value

		return current
	}

	next, ok := current[index].([]any)
	if !ok {
		next = make([]any, path[1]+1)
	}

	current[index] = setNestedValue(next, value, path[1:]...)

	return current
}

func ensureLen(items []any, length int) []any {
	if len(items) >= length {
		return items
	}

	extended := make([]any, length)
	copy(extended, items)

	return extended
}

func TestGetMenu(t *testing.T) {
	raw, err := os.ReadFile("testdata/menu_hoangs_kitchen.json")
	require.NoError(t, err)

	var menuNode any
	require.NoError(t, json.Unmarshal(raw, &menuNode))

	// The fixture is the subtree found at darray[125]; rebuild a darray
	// large enough to hold it at that position.
	darray := make([]any, 126)
	darray[125] = menuNode

	sections := getMenu(darray)
	require.Len(t, sections, 21)

	total := 0
	for _, s := range sections {
		total += len(s.Items)
	}

	require.Equal(t, 206, total)

	require.Equal(t, "SPRING ROLLS", sections[0].Name)
	require.Len(t, sections[0].Items, 6)
	require.Equal(t, MenuItem{
		Name:        "1. HOUSE-MADE FRIED SPRING ROLLS",
		Description: "(shrimp, minced pork, wood-ear mushroom, shiitake mushroom, onion, garlic, carrot, green onion, glass noodle, sweet potato, taro)",
	}, sections[0].Items[0])

	last := sections[len(sections)-1]
	require.Equal(t, "SOFT DRINKS", last.Name)
	require.Equal(t, MenuItem{Name: "Fanta", Description: "(Orange)"}, last.Items[4])
	require.Equal(t, MenuItem{Name: "Soda with Passion Fruit Juice"}, last.Items[len(last.Items)-1])
}

func TestGetMenuMissing(t *testing.T) {
	require.Nil(t, getMenu(nil))
	require.Nil(t, getMenu(make([]any, 200)))

	darray := make([]any, 126)
	darray[125] = []any{[]any{[]any{nil, []any{}}}}
	require.Nil(t, getMenu(darray))
}

func TestGetHighlights(t *testing.T) {
	raw, err := os.ReadFile("testdata/highlights_buddha_chay.json")
	require.NoError(t, err)

	var node any
	require.NoError(t, json.Unmarshal(raw, &node))

	// The fixture is the subtree found at darray[120]; rebuild a darray
	// large enough to hold it at that position.
	darray := make([]any, 121)
	darray[120] = node

	highlights := getHighlights(darray)
	require.Len(t, highlights, 19)

	first := highlights[0]
	require.Equal(t, "Mango Sticky Rice", first.Name)
	require.Equal(t, "/g/11m91ns0hc", first.ID)
	require.True(t, strings.HasPrefix(first.Source, "https://www.google.com/local/place/offerings?"), first.Source)
	require.Contains(t, first.Source, "oid=/g/11m91ns0hc")
	require.Contains(t, first.Source, "on=Mango+Sticky+Rice")
	require.Equal(t, 16, first.PhotoCount)
	require.Equal(t, 4, first.ReviewCount)
	require.Len(t, first.Photos, 3)

	for _, p := range first.Photos {
		require.True(t, strings.HasPrefix(p, "https://lh3.googleusercontent.com/"), p)
	}

	last := highlights[len(highlights)-1]
	require.Equal(t, "Five Colored Soup", last.Name)
	require.Equal(t, "/g/11vszpnfnq", last.ID)
	require.Contains(t, last.Source, "on=Five+Colored+Soup")
	require.Equal(t, 2, last.PhotoCount)
	require.Equal(t, 0, last.ReviewCount)
	require.Len(t, last.Photos, 1)

	for _, h := range highlights {
		require.NotEmpty(t, h.Name)
		require.NotEmpty(t, h.Source)
	}
}

func TestGetHighlightsMissing(t *testing.T) {
	require.Nil(t, getHighlights(nil))
	require.Nil(t, getHighlights(make([]any, 200)))

	darray := make([]any, 121)
	darray[120] = []any{nil, nil, nil, "", []any{[]any{1, []any{}}}}
	require.Nil(t, getHighlights(darray))
}
