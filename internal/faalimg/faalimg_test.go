package faalimg

import (
	"bytes"
	"encoding/json"
	"errors"
	"image/png"
	"os"
	"strings"
	"testing"
)

// fixtureHemistichs reads the faal fixture's hemistichs (verses[] text).
func fixtureHemistichs(t *testing.T) (string, []string) {
	t.Helper()
	b, err := os.ReadFile("../ganjoor/testdata/faal.json")
	if err != nil {
		t.Fatal(err)
	}
	var d struct {
		Title  string `json:"title"`
		Verses []struct {
			Text string `json:"text"`
		} `json:"verses"`
	}
	if err := json.Unmarshal(b, &d); err != nil {
		t.Fatal(err)
	}
	var hs []string
	for _, v := range d.Verses {
		hs = append(hs, v.Text)
	}
	return d.Title, hs
}

func TestRenderGhazal(t *testing.T) {
	r, err := New("", ShaperAuto)
	if err != nil {
		t.Fatal(err)
	}
	title, hs := fixtureHemistichs(t)
	out, err := r.Render("فال حافظ — "+title, hs)
	if err != nil {
		t.Fatal(err)
	}
	img, err := png.Decode(bytes.NewReader(out))
	if err != nil {
		t.Fatal(err)
	}
	if b := img.Bounds(); b.Dx() != width || b.Dy() < 9*60 {
		t.Fatalf("image %v for 9 بیت", b)
	}
	if path := os.Getenv("FAALIMG_OUT"); path != "" {
		_ = os.WriteFile(path, out, 0o644)
	}
}

// Every Persian letter, the Persian digits and the punctuation a ghazal uses
// must have a glyph in the font.
func TestFontCoversPersian(t *testing.T) {
	r, err := New("", ShaperAuto)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range []string{
		"ا ب پ ت ث ج چ ح خ د ذ ر ز ژ س ش ص ض ط ظ ع غ ف ق ک گ ل م ن و ه ی",
		"آ أ ؤ ئ ء ۀ هٔ ة ي ك",
		"۰۱۲۳۴۵۶۷۸۹ « » ، ؛ ؟ ! . :",
		"می‌دانم که می‌دانی", // ZWNJ
	} {
		if _, err := r.shape(s, 40); err != nil {
			t.Errorf("%q: %v", s, err)
		}
	}
}

func TestMissingGlyphsAreReported(t *testing.T) {
	r, _ := New("", ShaperAuto)
	_, err := r.Render("t", []string{"漢字"})
	if !errors.Is(err, ErrMissingGlyphs) {
		t.Fatalf("err = %v", err)
	}
	if err == nil || !strings.Contains(err.Error(), "漢字") {
		t.Fatal("the error should name the text")
	}
}

// With HarfBuzz the base letters of a Nastaliq word are raised by cursive
// attachment (the cascade); go-text leaves them on the baseline.
func TestHarfBuzzCascade(t *testing.T) {
	const noto = "fonts/NotoNastaliqUrdu.ttf" // the same font for both shapers
	hb, err := New(noto, ShaperHarfBuzz)
	if err != nil {
		t.Skip("hb-shape not installed:", err)
	}
	g, _ := New(noto, ShaperGo)
	if hb.Engine() != "harfbuzz" || g.Engine() != "go" {
		t.Fatal(hb.Engine(), g.Engine())
	}
	const word = "نسیم"
	a, err := hb.shape(word, 100)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := g.shape(word, 100)
	if len(a.Glyphs) != len(b.Glyphs) {
		t.Fatalf("glyph counts %d / %d", len(a.Glyphs), len(b.Glyphs))
	}
	raised := false
	for i := range a.Glyphs {
		if a.Glyphs[i].GlyphID != b.Glyphs[i].GlyphID {
			t.Fatalf("glyph %d differs", i)
		}
		raised = raised || a.Glyphs[i].YOffset != b.Glyphs[i].YOffset
	}
	if !raised {
		t.Fatal("no cascade offsets from HarfBuzz")
	}
	if _, err := hb.Render("t", []string{word, word}); err != nil {
		t.Fatal(err)
	}
}

func TestUnknownShaper(t *testing.T) {
	if _, err := New("", "cairo"); err == nil {
		t.Fatal("accepted an unknown shaper")
	}
}

// Both embedded fonts cover Persian, each with the shaper it is the default for.
func TestEmbeddedFontsCoverPersian(t *testing.T) {
	for _, sh := range []Shaper{ShaperGo, ShaperHarfBuzz} {
		r, err := New("", sh)
		if err != nil {
			t.Skip(err)
		}
		for _, s := range []string{"ا ب پ ت ث ج چ ح خ د ذ ر ز ژ س ش ص ض ط ظ ع غ ف ق ک گ ل م ن و ه ی", "۰۱۲۳۴۵۶۷۸۹ « » ، ؛ ؟ هٔ آ ئ"} {
			if _, err := r.shape(s, 40); err != nil {
				t.Errorf("%s: %v", sh, err)
			}
		}
	}
}

func TestDigitsReadLeftToRight(t *testing.T) {
	for in, want := range map[string]string{
		"غزل شمارهٔ ۱۷۶": "غزل شمارهٔ ۶۷۱", // reversed in logical order = read ۱۷۶ in an RTL run
		"سال 1403 و ۲٫۵": "سال 3041 و ۵٫۲",
		"بی‌عدد":         "بی‌عدد",
		"۱۲، ۳۴":         "۲۱، ۴۳", // the separator between numbers is not part of either
	} {
		if got := digitsLTR(in); got != want {
			t.Errorf("digitsLTR(%q) = %q, want %q", in, got, want)
		}
	}
}
