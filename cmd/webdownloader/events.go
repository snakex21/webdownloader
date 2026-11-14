package main

import (
	"github.com/asrock/webdownloader/internal/downloader"
)

// The payloads below are the JSON contract with app.js: the field names the
// UI reads are set by the json tags, so renaming a Go field in the
// downloader cannot silently break the window.

// progressPayload is the payload of a "progress" event.
type progressPayload struct {
	URL         string `json:"url,omitempty"`
	Status      string `json:"status"` // "downloading" | "error" | "done" | "progress"
	Depth       int    `json:"depth"`
	Pages       int    `json:"pages"`
	Assets      int    `json:"assets"`
	Attachments int    `json:"attachments"`
	Bytes       int64  `json:"bytes"`
	Queued      int    `json:"queued"`
	Transferred int64  `json:"transferred"`
	Current     string `json:"current,omitempty"`
}

func newProgressPayload(ev downloader.PageEvent) progressPayload {
	return progressPayload{
		URL:         ev.URL,
		Status:      ev.Status,
		Depth:       ev.Depth,
		Pages:       ev.Pages,
		Assets:      ev.Assets,
		Attachments: ev.Attachments,
		Bytes:       ev.Bytes,
		Queued:      ev.Queued,
		Transferred: ev.Transferred,
		Current:     ev.Current,
	}
}

// assetPayload is the payload of an "asset" event.
type assetPayload struct {
	URL  string `json:"url"`
	Kind string `json:"kind"` // "asset" | "attachment"
}

// errorPayload is the payload of an "error" event.
type errorPayload struct {
	URL   string `json:"url"`
	Error string `json:"error"`
}

// failedFilePayload is one entry of completePayload.FailedFiles.
type failedFilePayload struct {
	URL    string `json:"url"`
	Kind   string `json:"kind"`
	Status int    `json:"status,omitempty"`
}

// completePayload is the payload of the "complete" event that ends a run.
type completePayload struct {
	Pages       int    `json:"pages"`
	Assets      int    `json:"assets"`
	Attachments int    `json:"attachments"`
	Bytes       int64  `json:"bytes"`
	OutputDir   string `json:"outputDir,omitempty"`
	ReportPath  string `json:"reportPath,omitempty"`
	StartFile   string `json:"startFile,omitempty"`
	Cancelled   bool   `json:"cancelled"`
	// Forced is set when the cancel watchdog ended a run that did not stop.
	Forced               bool                `json:"forced,omitempty"`
	Failed               bool                `json:"failed"`
	FailKind             string              `json:"failKind,omitempty"`
	FailStatus           int                 `json:"failStatus,omitempty"`
	FailDetail           string              `json:"failDetail,omitempty"`
	Errors               int                 `json:"errors"`
	FailedFiles          []failedFilePayload `json:"failedFiles"`
	SkippedExternal      int                 `json:"skippedExternal"`
	SkippedExternalHosts []string            `json:"skippedExternalHosts"`
	SkippedTrackers      int                 `json:"skippedTrackers"`
	SkippedTrackerHosts  []string            `json:"skippedTrackerHosts"`
	NeedsServer          bool                `json:"needsServer"`
	Engine               string              `json:"engine,omitempty"`
	ElapsedMs            int64               `json:"elapsedMs"`
}

func newCompletePayload(s downloader.Summary) completePayload {
	p := completePayload{
		Pages:                s.Pages,
		Assets:               s.Assets,
		Attachments:          s.Attachments,
		Bytes:                s.Bytes,
		OutputDir:            s.OutputDir,
		ReportPath:           s.ReportPath,
		StartFile:            s.StartFile,
		Cancelled:            s.Cancelled,
		Failed:               s.Failed,
		FailKind:             s.FailKind,
		FailStatus:           s.FailStatus,
		FailDetail:           s.FailDetail,
		Errors:               s.Errors,
		FailedFiles:          []failedFilePayload{},
		SkippedExternal:      s.SkippedExternal,
		SkippedExternalHosts: nonNil(s.SkippedExternalHosts),
		SkippedTrackers:      s.SkippedTrackers,
		SkippedTrackerHosts:  nonNil(s.SkippedTrackerHosts),
		NeedsServer:          s.NeedsServer,
		Engine:               s.Engine,
		ElapsedMs:            s.Elapsed.Milliseconds(),
	}
	for _, f := range s.FailedFiles {
		p.FailedFiles = append(p.FailedFiles, failedFilePayload{URL: f.URL, Kind: f.Kind, Status: f.Status})
	}
	return p
}

// nonNil keeps empty lists as [] (not null) in JSON.
func nonNil(list []string) []string {
	if list == nil {
		return []string{}
	}
	return list
}
