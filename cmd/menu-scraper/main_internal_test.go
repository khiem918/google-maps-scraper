package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/gosom/google-maps-scraper/exiter"
	"github.com/gosom/google-maps-scraper/gmaps"
)

const sampleResults = `{"title":"Busan Korean Food","address":"Biên Hòa, Đồng Nai","data_id":"0x1:0x2","cid":"10750081369738622822","longitude":106.8}
{"title":"","address":"no title"}

{"title":"Sành Thái","menu_items":[{"name":"Old","items":[{"name":"Dish"}]}]}
`

func TestReadEntries(t *testing.T) {
	entries, err := readEntries(strings.NewReader(sampleResults))

	require.NoError(t, err)
	require.Len(t, entries, 3)
	require.Equal(t, "Busan Korean Food", entries[0].Title)
	require.Equal(t, "0x1:0x2", entries[0].DataID)
	require.Equal(t, "10750081369738622822", entries[0].Cid)
	require.InDelta(t, 106.8, entries[0].Longtitude, 1e-9)
	require.Len(t, entries[2].MenuItems, 1, "existing Maps data is kept")
}

func TestReadEntriesRejectsCSV(t *testing.T) {
	_, err := readEntries(strings.NewReader("input_id,link,title\n1,x,Busan\n"))

	require.ErrorContains(t, err, "place 1")
}

func TestMenuJobsSkipsUntitled(t *testing.T) {
	entries, err := readEntries(strings.NewReader(sampleResults))
	require.NoError(t, err)

	jobs, skipped := menuJobs(entries, "vi", gmaps.NewMenuBreaker(3, 5), exiter.New(), newRunReport())

	require.Equal(t, 1, skipped)
	require.Len(t, jobs, 2)

	job, ok := jobs[0].(*gmaps.MenuJob)
	require.True(t, ok)
	require.Same(t, entries[0], job.Entry)
	require.Equal(t, "Busan Korean Food Biên Hòa, Đồng Nai", job.URLParams["q"])
	require.Equal(t, "vi", job.URLParams["hl"])
	require.True(t, job.UseInResults())
	require.NotNil(t, job.Breaker)
	require.NotNil(t, job.ExitMonitor)
	require.NotNil(t, job.OnOutcome)
}

func TestCheckResultsPath(t *testing.T) {
	dir := t.TempDir()
	existing := filepath.Join(dir, "menu.json")
	empty := filepath.Join(dir, "empty.json")
	input := filepath.Join(dir, "places.json")

	require.NoError(t, os.WriteFile(existing, []byte(`{"title":"Done"}`+"\n"), 0o600))
	require.NoError(t, os.WriteFile(empty, nil, 0o600))

	for _, tc := range []struct {
		name      string
		results   string
		overwrite bool
		wantErr   string
	}{
		{name: "stdout", results: "stdout"},
		{name: "new file", results: filepath.Join(dir, "new.json")},
		{name: "empty file", results: empty},
		{name: "earlier results", results: existing, wantErr: "-overwrite"},
		{name: "earlier results with overwrite", results: existing, overwrite: true},
		{name: "input", results: input, overwrite: true, wantErr: "-results must differ from -input"},
		{name: "input via another spelling", results: dir + "/./places.json", overwrite: true, wantErr: "-results must differ from -input"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := checkResultsPath(tc.results, input, tc.overwrite)

			if tc.wantErr == "" {
				require.NoError(t, err)

				return
			}

			require.ErrorContains(t, err, tc.wantErr)
		})
	}
}

func TestRunKeepsEarlierResults(t *testing.T) {
	dir := t.TempDir()
	input := filepath.Join(dir, "places.json")
	results := filepath.Join(dir, "menu.json")

	require.NoError(t, os.WriteFile(input, []byte(sampleResults), 0o600))
	require.NoError(t, os.WriteFile(results, []byte(`{"title":"Done"}`+"\n"), 0o600))

	err := run(context.Background(), &config{input: input, results: results, concurrency: 1})

	require.ErrorContains(t, err, "-overwrite")

	kept, readErr := os.ReadFile(results)
	require.NoError(t, readErr)
	require.JSONEq(t, `{"title":"Done"}`, string(kept), "the earlier results are untouched")
}
