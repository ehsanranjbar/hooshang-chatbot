package ganjoor

import (
	"context"
	_ "embed"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

//go:embed testdata/faal.json
var faalJSON string

//go:embed testdata/random.json
var randomJSON string

// fakeClock is an injectable clock.
type fakeClock struct {
	mu      sync.Mutex
	current time.Time
}

func (f *fakeClock) now() time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.current
}

func (f *fakeClock) advance(d time.Duration) {
	f.mu.Lock()
	f.current = f.current.Add(d)
	f.mu.Unlock()
}

// newTestServer serves the captured fixtures at the real API paths and
// counts requests per path.
func newTestServer() (*httptest.Server, *map[string]int) {
	counts := map[string]int{}
	var mu sync.Mutex
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		counts[r.URL.Path+"?"+r.URL.RawQuery]++
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasPrefix(r.URL.Path, "/api/ganjoor/hafez/faal"):
			_, _ = w.Write([]byte(faalJSON))
		case strings.HasPrefix(r.URL.Path, "/api/ganjoor/poem/random"):
			_, _ = w.Write([]byte(randomJSON))
		default:
			http.NotFound(w, r)
		}
	}))
	m := &counts
	return ts, m
}

func countAt(m *map[string]int, sub string) int {
	for k, v := range *m {
		if strings.Contains(k, sub) {
			return v
		}
	}
	return 0
}

func TestFaalParsesFields(t *testing.T) {
	ts, _ := newTestServer()
	defer ts.Close()
	c := New(ts.URL, ts.Client())

	p, err := c.Faal(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if p.ID != 2603 {
		t.Errorf("ID = %d, want 2603 (from fixture)", p.ID)
	}
	if p.FullTitle == "" || p.Title == "" {
		t.Errorf("titles empty: %+v", p)
	}
	if p.URL != "https://ganjoor.net/hafez/ghazal/sh474" {
		t.Errorf("URL = %q", p.URL)
	}
	if len(p.Hemistichs) == 0 || len(p.Hemistichs)%2 != 0 {
		t.Errorf("Hemistichs = %d entries, want an even number > 0", len(p.Hemistichs))
	}
	for _, h := range p.Hemistichs {
		if strings.Contains(h, "\u00A0") {
			t.Errorf("hemistich still contains NBSP: %q", h)
		}
		if strings.TrimSpace(h) != h {
			t.Errorf("hemistich not trimmed: %q", h)
		}
	}
}

func TestSummaryPrefixStripped(t *testing.T) {
	ts, _ := newTestServer()
	defer ts.Close()
	c := New(ts.URL, ts.Client())

	p, err := c.Faal(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if strings.HasPrefix(p.Summary, "هوش مصنوعی") {
		t.Errorf("summary still has the AI prefix: %q", p.Summary)
	}
	if p.Summary == "" {
		t.Error("summary is empty, fixture has one")
	}
}

func TestNBSPInPlainText(t *testing.T) {
	// A minimal hand-built response with a known NBSP and a stray empty line.
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"id":1,"title":"ت","fullTitle":"ف","fullUrl":"/x","plainText":"خط اول\u00A0با نشانه\r\n\r\nخط دوم","poemSummary":null}`))
	}))
	defer ts.Close()
	c := New(ts.URL, ts.Client())

	p, err := c.Faal(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(strings.Join(p.Hemistichs, ""), "\u00A0") {
		t.Error("NBSP was not replaced with a space")
	}
	if p.Summary != "" {
		t.Errorf("null summary should stay empty, got %q", p.Summary)
	}
	// Line 1 is a single hemistich (no midpoint space); line 2 is one hemistich.
	for _, h := range p.Hemistichs {
		if h == "" {
			t.Error("empty hemistich from blank line")
		}
	}
}

func TestRandomPoem(t *testing.T) {
	ts, _ := newTestServer()
	defer ts.Close()
	c := New(ts.URL, ts.Client())

	p, err := c.RandomPoem(context.Background(), 7)
	if err != nil {
		t.Fatal(err)
	}
	if p.ID != 9498 {
		t.Errorf("ID = %d, want 9498 (from fixture)", p.ID)
	}
	if !strings.Contains(p.URL, "saadi") {
		t.Errorf("URL = %q, want saadi path", p.URL)
	}
}

func TestErrorOnNon200(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer ts.Close()
	c := New(ts.URL, ts.Client())
	if _, err := c.Faal(context.Background()); err == nil || !strings.Contains(err.Error(), "500") {
		t.Errorf("err = %v, want an error mentioning 500", err)
	}
}

func TestCacheHitAndExpiry(t *testing.T) {
	ts, counts := newTestServer()
	defer ts.Close()
	fc := &fakeClock{current: time.Now()}
	c := New(ts.URL, ts.Client())
	c.setNow(fc.now)

	ctx := context.Background()
	if _, err := c.Faal(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Faal(ctx); err != nil {
		t.Fatal(err)
	}
	if n := countAt(counts, "hafez/faal"); n != 1 {
		t.Fatalf("faal requests after 2 calls within TTL = %d, want 1 (cache hit)", n)
	}
	// Same poet: second call is a cache hit; a different poet is a miss.
	if _, err := c.RandomPoem(ctx, 7); err != nil {
		t.Fatal(err)
	}
	if _, err := c.RandomPoem(ctx, 7); err != nil {
		t.Fatal(err)
	}
	if _, err := c.RandomPoem(ctx, 2); err != nil {
		t.Fatal(err)
	}
	if n := countAt(counts, "poetId=7"); n != 1 {
		t.Errorf("poetId=7 requests = %d, want 1 (cache hit)", n)
	}
	if n := countAt(counts, "poetId=2"); n != 1 {
		t.Errorf("poetId=2 requests = %d, want 1 (separate cache entry)", n)
	}
	// After the TTL the next call re-fetches.
	fc.advance(cacheTTL + time.Second)
	if _, err := c.Faal(ctx); err != nil {
		t.Fatal(err)
	}
	if n := countAt(counts, "hafez/faal"); n != 2 {
		t.Errorf("faal requests after TTL expiry = %d, want 2", n)
	}
}

func TestDefaultBaseURL(t *testing.T) {
	c := New("", nil)
	if c.base != DefaultBaseURL {
		t.Errorf("base = %q, want %q", c.base, DefaultBaseURL)
	}
}

func TestReadsTestdataFilesExist(t *testing.T) {
	// The fixtures must be present on disk (captured once from the live API).
	for _, f := range []string{"testdata/faal.json", "testdata/random.json"} {
		if _, err := os.Stat(filepath.Join(".", f)); err != nil {
			t.Errorf("missing %s: %v", f, err)
		}
	}
}

// Each plainText line is one hemistich: the faal fixture (ghazal 474) has 18
// lines, i.e. 9 couplets, and its first couplet is the first two lines.
func TestFaalHemistichsAreLines(t *testing.T) {
	srv, _ := newTestServer()
	defer srv.Close()
	p, err := New(srv.URL, nil).Faal(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Hemistichs) != 18 {
		t.Fatalf("Hemistichs = %d, want 18", len(p.Hemistichs))
	}
	want := "📜 " + p.FullTitle + "\n\n" + p.Hemistichs[0] + "\n" + p.Hemistichs[1] + "\n\n…"
	if got := Format(p, 1); got != want {
		t.Fatalf("Format(p, 1) = %q, want %q", got, want)
	}
	if !strings.HasPrefix(p.Hemistichs[0], "هواخواه توام جانا و می‌دانم") || !strings.HasPrefix(p.Hemistichs[1], "که هم نادیده") {
		t.Fatalf("first couplet = %q / %q", p.Hemistichs[0], p.Hemistichs[1])
	}
}

func TestPoetFromFullTitle(t *testing.T) {
	srv, _ := newTestServer()
	defer srv.Close()
	p, err := New(srv.URL, nil).RandomPoem(context.Background(), 7)
	if err != nil {
		t.Fatal(err)
	}
	if p.Poet != "سعدی" {
		t.Fatalf("Poet = %q", p.Poet)
	}
}
