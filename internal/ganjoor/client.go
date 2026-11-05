// Package ganjoor fetches Hafez faals and random poems from the Ganjoor API.
package ganjoor

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"
)

// DefaultBaseURL is the public Ganjoor API base.
const DefaultBaseURL = "https://api.ganjoor.net"

// cacheTTL is how long a fetched poem stays cached.
const cacheTTL = 30 * time.Second

// Poem is the subset of a Ganjoor poem that hooshang uses.
type Poem struct {
	ID         int64
	Title      string
	FullTitle  string
	Poet       string // the first part of FullTitle, e.g. «سعدی»
	URL        string
	Hemistichs []string
	Summary    string
}

// Client calls the Ganjoor API. Results are cached for 30s: one cached faal
// and one per poetID.
type Client struct {
	base string
	hc   *http.Client

	mu       sync.Mutex
	faal     *Poem
	faalAt   time.Time
	random   map[int]*Poem
	randomAt map[int]time.Time
	now      func() time.Time
}

// New returns a Client. An empty baseURL uses https://api.ganjoor.net.
// A nil hc uses http.DefaultClient.
func New(baseURL string, hc *http.Client) *Client {
	if baseURL == "" {
		baseURL = DefaultBaseURL
	}
	if hc == nil {
		hc = http.DefaultClient
	}
	return &Client{
		base:     strings.TrimRight(baseURL, "/"),
		hc:       hc,
		random:   make(map[int]*Poem),
		randomAt: make(map[int]time.Time),
		now:      time.Now,
	}
}

// Faal returns a random Hafez ghazal, cached for 30s.
func (c *Client) Faal(ctx context.Context) (*Poem, error) {
	c.mu.Lock()
	if c.faal != nil && c.now().Sub(c.faalAt) < cacheTTL {
		p := c.faal
		c.mu.Unlock()
		return p, nil
	}
	c.mu.Unlock()

	p, err := c.get(ctx, "/api/ganjoor/hafez/faal")
	if err != nil {
		return nil, err
	}
	c.mu.Lock()
	c.faal = p
	c.faalAt = c.now()
	c.mu.Unlock()
	return p, nil
}

// RandomPoem returns a random poem of the given poetID, cached for 30s per poet.
func (c *Client) RandomPoem(ctx context.Context, poetID int) (*Poem, error) {
	c.mu.Lock()
	if p, ok := c.random[poetID]; ok && c.now().Sub(c.randomAt[poetID]) < cacheTTL {
		c.mu.Unlock()
		return p, nil
	}
	c.mu.Unlock()

	p, err := c.get(ctx, fmt.Sprintf("/api/ganjoor/poem/random?poetId=%d", poetID))
	if err != nil {
		return nil, err
	}
	c.mu.Lock()
	c.random[poetID] = p
	c.randomAt[poetID] = c.now()
	c.mu.Unlock()
	return p, nil
}

// setNow overrides the clock; tests use a fake clock to test the cache.
func (c *Client) setNow(now func() time.Time) { c.now = now }

// apiPoem holds only the fields from the response that hooshang uses.
type apiPoem struct {
	ID          int64   `json:"id"`
	Title       string  `json:"title"`
	FullTitle   string  `json:"fullTitle"`
	FullURL     string  `json:"fullUrl"`
	PlainText   string  `json:"plainText"`
	PoemSummary *string `json:"poemSummary"` // a string or null
	Verses      []struct {
		VOrder        int    `json:"vOrder"`
		VersePosition int    `json:"versePosition"` // 0/1 = right/left hemistich; others are bands or prose
		Text          string `json:"text"`
	} `json:"verses"`
}

// get fetches one poem path and parses it into a Poem.
func (c *Client) get(ctx context.Context, path string) (*Poem, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+path, nil)
	if err != nil {
		return nil, fmt.Errorf("ganjoor: %w", err)
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		return nil, fmt.Errorf("ganjoor: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 300))
		return nil, fmt.Errorf("ganjoor: %s: %d: %s", path, resp.StatusCode, string(body))
	}
	var ap apiPoem
	if err := json.NewDecoder(resp.Body).Decode(&ap); err != nil {
		return nil, fmt.Errorf("ganjoor: decode %s: %w", path, err)
	}
	return parsePoem(ap), nil
}

// parsePoem converts the API subset into a Poem: plainText lines are split
// on \r\n or \n, NBSPs are replaced by spaces, lines are trimmed and empty
// lines dropped; every line is one hemistich (verses[] confirms this, by
// versePosition 0/1); the "هوش مصنوعی: " prefix is stripped from Summary; URL is
// the fullUrl path on ganjoor.net.
func parsePoem(ap apiPoem) *Poem {
	p := &Poem{
		ID:        ap.ID,
		Title:     ap.Title,
		FullTitle: ap.FullTitle,
		Poet:      strings.TrimSpace(strings.SplitN(ap.FullTitle, "»", 2)[0]),
		URL:       "https://ganjoor.net" + ap.FullURL,
	}
	// verses[] has the properly typeset text (ZWNJs, punctuation); plainText
	// is the fallback. Both have one hemistich (mesra) per entry/line.
	lines := make([]string, 0, len(ap.Verses))
	verses := ap.Verses
	sort.SliceStable(verses, func(i, j int) bool { return verses[i].VOrder < verses[j].VOrder })
	for _, v := range verses {
		if v.VersePosition != 0 && v.VersePosition != 1 {
			lines = nil // not a plain couplet poem: use plainText as is
			break
		}
		lines = append(lines, v.Text)
	}
	if len(lines) == 0 {
		lines = strings.FieldsFunc(ap.PlainText, func(r rune) bool { return r == '\r' || r == '\n' })
	}
	for _, line := range lines {
		line = strings.TrimSpace(strings.ReplaceAll(line, "\u00A0", " "))
		if line == "" {
			continue
		}
		p.Hemistichs = append(p.Hemistichs, line)
	}
	if ap.PoemSummary != nil {
		p.Summary = strings.TrimSpace(strings.TrimPrefix(*ap.PoemSummary, "هوش مصنوعی: "))
	}
	return p
}
