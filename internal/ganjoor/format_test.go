package ganjoor

import (
	"strings"
	"testing"
)

func TestFormatCouplets(t *testing.T) {
	p := &Poem{
		FullTitle:  "حافظ » غزل ۱",
		Hemistichs: []string{"h1a", "h1b", "h2a", "h2b", "h3a", "h3b"},
	}
	out := Format(p, 0)
	want := "📜 حافظ » غزل ۱\n\nh1a\nh1b\n\nh2a\nh2b\n\nh3a\nh3b"
	if out != want {
		t.Errorf("Format(0):\n%q\nwant:\n%q", out, want)
	}
}

func TestFormatTruncatesAndAppendsEllipsis(t *testing.T) {
	p := &Poem{
		FullTitle:  "T",
		Hemistichs: []string{"h1a", "h1b", "h2a", "h2b", "h3a", "h3b"},
	}
	out := Format(p, 2)
	if !strings.Contains(out, "h1a") || !strings.Contains(out, "h2b") {
		t.Errorf("missing kept couplets: %q", out)
	}
	if strings.Contains(out, "h3a") {
		t.Errorf("truncation failed, h3a present: %q", out)
	}
	if !strings.HasSuffix(out, "…") {
		t.Errorf("expected trailing …, got %q", out)
	}
}

func TestFormatNoTruncationForZero(t *testing.T) {
	p := &Poem{Hemistichs: []string{"a", "b", "c", "d", "e", "f", "g", "h"}}
	out := Format(p, 0)
	if strings.Contains(out, "…") {
		t.Errorf("maxCouplets=0 must not truncate: %q", out)
	}
}

func TestFormatSingleHemistichLast(t *testing.T) {
	// Odd count: last hemistich has no partner.
	p := &Poem{Hemistichs: []string{"a", "b", "c"}}
	out := Format(p, 0)
	if !strings.Contains(out, "\nc") {
		t.Errorf("single trailing hemistich missing: %q", out)
	}
}

func TestFormatEmptyPoem(t *testing.T) {
	out := Format(&Poem{}, 0)
	if strings.TrimSpace(out) != "" {
		t.Errorf("empty poem should render as blank-ish, got %q", out)
	}
}

func TestExcerpt(t *testing.T) {
	p := &Poem{Hemistichs: []string{"a1", "a2", "b1", "b2", "c1", "c2"}}
	if got := Excerpt(p, 2); got != "a1\na2\n\nb1\nb2" {
		t.Fatalf("Excerpt(2) = %q", got)
	}
	if got := Excerpt(&Poem{Hemistichs: []string{"x"}}, 2); got != "x" {
		t.Fatalf("one hemistich = %q", got)
	}
}
