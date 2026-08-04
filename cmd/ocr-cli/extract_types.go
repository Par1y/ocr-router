package main

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// schemaVersion is the version of the per-page JSON / manifest contract emitted
// by `extract`. Bump when the on-disk shape changes incompatibly.
const schemaVersion = 1

// pageStatus values used in both per-page JSON and manifest entries.
const (
	statusOK        = "ok"
	statusFailed    = "failed"
	statusCancelled = "cancelled"
)

// manifest top-level status values.
const (
	manifestRunning   = "running"
	manifestOK        = "ok"
	manifestPartial   = "partial"
	manifestCancelled = "cancelled"
	manifestError     = "error"
)

// PageJSON is the per-page result written to <base>-%04d.json for every page,
// success or failure. It is the structured unit the translator workbench
// ingests. `error` is a pointer so it serializes as JSON null when absent.
type PageJSON struct {
	Page           int     `json:"page"`
	Status         string  `json:"status"`
	Text           string  `json:"text"`
	Score          float64 `json:"score"`
	ScorePresent   bool    `json:"score_present"`
	Provider       string  `json:"provider"`
	Fallback       bool    `json:"fallback"`
	QualityWarning bool    `json:"quality_warning"`
	Blank          bool    `json:"blank"`
	BestScore      float64 `json:"best_score"`
	Error          *string `json:"error"`
	SourceFile     string  `json:"source_file"`
	Image          string  `json:"image"`
}

// ManifestPage is the per-page summary embedded in the manifest's pages array.
type ManifestPage struct {
	Page   int     `json:"page"`
	Status string  `json:"status"`
	Score  float64 `json:"score"`
	File   string  `json:"file"`
	Error  *string `json:"error"`
}

// Manifest is the single read-entry the workbench consumes per input. It is
// updated atomically after every page so a crashed/cancelled run still leaves a
// consistent, reconcilable file behind.
type Manifest struct {
	SchemaVersion    int            `json:"schema_version"`
	Source           string         `json:"source"`
	Type             string         `json:"type"` // "pdf" | "image"
	PagesTotal       int            `json:"pages_total"`
	PagesAttempted   int            `json:"pages_attempted"`
	PagesOK          int            `json:"pages_ok"`
	PagesFailed      int            `json:"pages_failed"`
	WindowSize       int            `json:"window_size"`
	PageWorkers      int            `json:"page_workers"`
	EvaluatorEnabled bool           `json:"evaluator_enabled"`
	Provider         string         `json:"provider"`
	Status           string         `json:"status"`
	StartedAt        string         `json:"started_at"`
	FinishedAt       string         `json:"finished_at"`
	ElapsedMs        int64          `json:"elapsed_ms"`
	Pages            []ManifestPage `json:"pages"`
}

// manifestWriter owns a Manifest and persists it atomically. It is safe for
// concurrent use by page-level OCR workers. When a manifest already exists at
// path for the same source, its page entries are loaded so partial-page and
// skip-existing re-runs MERGE into the prior state rather than truncating it.
type manifestWriter struct {
	mu       sync.Mutex
	path     string
	m        *Manifest
	docTotal int // known document page count (0 = unknown)
}

func newManifestWriter(path string, base *Manifest, docTotal int) *manifestWriter {
	mw := &manifestWriter{path: path, m: base, docTotal: docTotal}
	// Carry forward previous page entries when re-running against the same
	// source (partial --pages re-OCR, --skip-existing continuation).
	if data, err := os.ReadFile(path); err == nil {
		var prev Manifest
		if json.Unmarshal(data, &prev) == nil && prev.Source == base.Source {
			mw.m.Pages = prev.Pages
		}
	}
	mw.recountLocked()
	return mw
}

// upsertPage inserts or replaces the entry for p.Page, recomputes counts, and
// flushes the manifest to disk atomically.
func (mw *manifestWriter) upsertPage(p ManifestPage) {
	mw.mu.Lock()
	defer mw.mu.Unlock()
	replaced := false
	for i := range mw.m.Pages {
		if mw.m.Pages[i].Page == p.Page {
			mw.m.Pages[i] = p
			replaced = true
			break
		}
	}
	if !replaced {
		mw.m.Pages = append(mw.m.Pages, p)
	}
	mw.recountLocked()
	mw.flushLocked()
}

// finalize sets the terminal status/timing and flushes one last time.
func (mw *manifestWriter) finalize(status string, started time.Time) {
	mw.mu.Lock()
	defer mw.mu.Unlock()
	mw.m.Status = status
	mw.m.FinishedAt = time.Now().Format(time.RFC3339)
	mw.m.ElapsedMs = time.Since(started).Milliseconds()
	mw.recountLocked()
	mw.flushLocked()
}

// counts returns (ok, failed) computed from the current page entries.
func (mw *manifestWriter) counts() (ok, failed int) {
	mw.mu.Lock()
	defer mw.mu.Unlock()
	return mw.m.PagesOK, mw.m.PagesFailed
}

func (mw *manifestWriter) recountLocked() {
	sort.Slice(mw.m.Pages, func(i, j int) bool { return mw.m.Pages[i].Page < mw.m.Pages[j].Page })
	ok, failed := 0, 0
	for _, p := range mw.m.Pages {
		switch p.Status {
		case statusOK:
			ok++
		case statusFailed:
			failed++
		}
	}
	mw.m.PagesOK = ok
	mw.m.PagesFailed = failed
	mw.m.PagesAttempted = len(mw.m.Pages)
	total := mw.docTotal
	if len(mw.m.Pages) > total {
		total = len(mw.m.Pages)
	}
	mw.m.PagesTotal = total
	mw.m.SchemaVersion = schemaVersion
}

// flushLocked writes the manifest to a temp file then renames it into place so
// a reader never observes a half-written manifest.
func (mw *manifestWriter) flushLocked() {
	data, err := json.MarshalIndent(mw.m, "", "  ")
	if err != nil {
		return
	}
	tmp := mw.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0644); err != nil {
		return
	}
	_ = os.Rename(tmp, mw.path)
}

// progressEmitter serializes progress output. json mode writes JSONL to stdout
// (the machine-readable contract); text mode writes human ✓/✗ lines to stderr;
// none is silent. It is safe for concurrent workers.
type progressEmitter struct {
	mu   sync.Mutex
	mode string // "json" | "text" | "none"
}

func newProgressEmitter(mode string) *progressEmitter { return &progressEmitter{mode: mode} }

func (p *progressEmitter) emitJSON(obj map[string]interface{}) {
	data, err := json.Marshal(obj)
	if err != nil {
		return
	}
	fmt.Fprintln(os.Stdout, string(data))
}

func (p *progressEmitter) start(source string, pagesTotal int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	switch p.mode {
	case "json":
		p.emitJSON(map[string]interface{}{"event": "start", "source": source, "pages_total": pagesTotal})
	case "text":
		fmt.Fprintf(os.Stderr, "[extract] %s: %d pages\n", source, pagesTotal)
	}
}

func (p *progressEmitter) page(page int, status string, score float64, scorePresent bool, done, total int, errMsg string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	switch p.mode {
	case "json":
		obj := map[string]interface{}{"event": "page", "page": page, "status": status, "done": done, "total": total}
		if scorePresent {
			obj["score"] = score
		}
		if errMsg != "" {
			obj["error"] = errMsg
		}
		p.emitJSON(obj)
	case "text":
		if status == statusOK {
			fmt.Fprintf(os.Stderr, "✓ page %d (%d/%d)\n", page, done, total)
		} else {
			fmt.Fprintf(os.Stderr, "✗ page %d (%d/%d): %s\n", page, done, total, errMsg)
		}
	}
}

func (p *progressEmitter) done(source string, pagesOK, pagesFailed int, manifest string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	switch p.mode {
	case "json":
		p.emitJSON(map[string]interface{}{
			"event": "done", "source": source,
			"pages_ok": pagesOK, "pages_failed": pagesFailed, "manifest": manifest,
		})
	case "text":
		fmt.Fprintf(os.Stderr, "[extract] %s done: %d ok, %d failed -> %s\n", source, pagesOK, pagesFailed, manifest)
	}
}

// parsePages parses a page-set spec like "3,7,10-12" into a sorted, de-duplicated
// slice of 1-based page numbers. An empty spec yields a nil slice (meaning "all
// pages"). Ranges may be given low-high or high-low.
func parsePages(spec string) ([]int, error) {
	spec = strings.TrimSpace(spec)
	if spec == "" {
		return nil, nil
	}
	set := map[int]bool{}
	for _, part := range strings.Split(spec, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		if strings.Contains(part, "-") {
			bounds := strings.SplitN(part, "-", 2)
			lo, err1 := strconv.Atoi(strings.TrimSpace(bounds[0]))
			hi, err2 := strconv.Atoi(strings.TrimSpace(bounds[1]))
			if err1 != nil || err2 != nil {
				return nil, fmt.Errorf("invalid page range %q", part)
			}
			if lo > hi {
				lo, hi = hi, lo
			}
			if lo < 1 {
				return nil, fmt.Errorf("page numbers must be >= 1: %q", part)
			}
			for pg := lo; pg <= hi; pg++ {
				set[pg] = true
			}
		} else {
			pg, err := strconv.Atoi(part)
			if err != nil {
				return nil, fmt.Errorf("invalid page number %q", part)
			}
			if pg < 1 {
				return nil, fmt.Errorf("page numbers must be >= 1: %q", part)
			}
			set[pg] = true
		}
	}
	out := make([]int, 0, len(set))
	for pg := range set {
		out = append(out, pg)
	}
	sort.Ints(out)
	return out, nil
}

// contiguousRuns groups a sorted, de-duplicated page slice into [lo,hi] runs of
// consecutive page numbers, so a sparse set (e.g. 1,3) renders only the needed
// pages instead of the whole span.
func contiguousRuns(pages []int) [][2]int {
	var runs [][2]int
	for i := 0; i < len(pages); {
		lo := pages[i]
		hi := lo
		j := i + 1
		for j < len(pages) && pages[j] == hi+1 {
			hi = pages[j]
			j++
		}
		runs = append(runs, [2]int{lo, hi})
		i = j
	}
	return runs
}
