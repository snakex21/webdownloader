package main

import (
	"encoding/json"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/asrock/webdownloader/internal/downloader"
)

func jsonKeys(t *testing.T, v any) map[string]bool {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatal(err)
	}
	keys := map[string]bool{}
	for k := range m {
		keys[k] = true
	}
	return keys
}

// The UI reads event payloads by their JSON names: every field app.js
// reads must be one the Go payload sends.
func TestAppJSReadsOnlyPayloadFields(t *testing.T) {
	src, err := os.ReadFile("../../internal/webui/web/app.js")
	if err != nil {
		t.Fatal(err)
	}
	js := string(src)
	// block returns the listener or function starting at start, up to its
	// closing brace at the start of a line.
	end := regexp.MustCompile(`\n\}\)?;?\r?\n`)
	block := func(start string) string {
		i := strings.Index(js, start)
		if i < 0 {
			t.Fatalf("app.js lacks %q", start)
		}
		rest := js[i:]
		return rest[:end.FindStringIndex(rest)[0]]
	}
	fullSummary := downloader.Summary{
		Pages: 1, Assets: 1, Attachments: 1, Bytes: 1, OutputDir: "o", ReportPath: "r", StartFile: "s",
		Cancelled: true, Failed: true, FailKind: "http", FailStatus: 404, FailDetail: "d", Errors: 1,
		FailedFiles:     []downloader.FailedFile{{URL: "u", Kind: "http", Status: 404}},
		SkippedExternal: 1, SkippedTrackers: 1, NeedsServer: true, Engine: "e", Elapsed: time.Second,
	}
	full := newCompletePayload(fullSummary)
	full.Forced = true
	cases := []struct {
		name, code, pattern string
		payload             any
	}{
		{"progress", block("listeners.progress.push"), `\bd\.(\w+)`, newProgressPayload(downloader.PageEvent{URL: "u", Current: "c"})},
		{"asset", block("listeners.asset.push"), `\bd\.(\w+)`, assetPayload{}},
		{"error", block("listeners.error.push"), `\bd\.(\w+)`, errorPayload{}},
		{"complete", block("async function finishRun"), `\bsummary\.(\w+)`, full},
		{"failedFiles", block("function failedNote"), `\bf\.(\w+)`, failedFilePayload{Status: 1}},
		{"folderInfo", block("async function deleteHistoryItem"), `\binfo\.(\w+)`, folderInfo{}},
		{"deleteFolder", block("async function deleteHistoryItem"), `\bresult\.(\w+)`, deleteResult{Busy: true, Error: "e"}},
	}
	for _, tc := range cases {
		keys := jsonKeys(t, tc.payload)
		matches := regexp.MustCompile(tc.pattern).FindAllStringSubmatch(tc.code, -1)
		if len(matches) == 0 {
			t.Errorf("%s: no field reads found", tc.name)
		}
		for _, m := range matches {
			if !keys[m[1]] {
				t.Errorf("%s: app.js reads %q, payload has %v", tc.name, m[1], keys)
			}
		}
	}
}

// Empty lists travel as [] so the UI never sees null.
func TestCompletePayloadListsAreNeverNull(t *testing.T) {
	data, err := json.Marshal(newCompletePayload(downloader.Summary{}))
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{`"failedFiles":[]`, `"skippedExternalHosts":[]`, `"skippedTrackerHosts":[]`} {
		if !strings.Contains(string(data), key) {
			t.Errorf("payload lacks %s: %s", key, data)
		}
	}
}
