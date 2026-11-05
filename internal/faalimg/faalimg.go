// Package faalimg renders a poem as an image in a Nastaliq font: a parchment
// card with the title at the top and one بیت per row, the first hemistich in
// the right column and the second in the left, as a ghazal is printed.
//
// Nastaliq needs full OpenType shaping (contextual forms, the cascading
// baseline), which go-text/typesetting's HarfBuzz port does in pure Go.
package faalimg

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"unicode"

	"github.com/go-text/render"
	"github.com/go-text/typesetting/di"
	"github.com/go-text/typesetting/font"
	"github.com/go-text/typesetting/language"
	"github.com/go-text/typesetting/shaping"
	"golang.org/x/image/math/fixed"
)

// The embedded fonts, both under the SIL Open Font License (fonts/*-OFL.txt)
// and both covering the Persian letters (پ چ ژ گ ک ی):
//   - Gulzar (by Borna Izadpanah) is the default with HarfBuzz: a calligraphic
//     Nastaliq with clean dot placement. Its contextual shaping is beyond go-text.
//   - Noto Nastaliq Urdu is the default with go-text, which shapes it correctly
//     apart from the cascade.
var (
	//go:embed fonts/Gulzar-Regular.ttf
	gulzarFont []byte
	//go:embed fonts/NotoNastaliqUrdu.ttf
	notoFont []byte
)

// Layout, in pixels.
const (
	width     = 1600
	margin    = 90  // outer margin, inside the frame
	gutter    = 90  // between the two columns
	maxSize   = 58  // poem font size, shrunk to fit the longest hemistich
	minSize   = 26  //
	titleSize = 40  //
	refSize   = 100 // size used to measure widths, which scale linearly
)

var (
	paper  = color.RGBA{0xF6, 0xEC, 0xD6, 0xFF}
	frame  = color.RGBA{0x8B, 0x6B, 0x3D, 0xFF}
	ink    = color.RGBA{0x2E, 0x1C, 0x0E, 0xFF}
	accent = color.RGBA{0x7A, 0x2E, 0x1E, 0xFF}
)

// ErrMissingGlyphs is returned when the font cannot draw some of the text.
var ErrMissingGlyphs = errors.New("faalimg: the font has no glyph for some characters")

// Renderer renders poems. It is safe for concurrent use.
type Renderer struct {
	mu     sync.Mutex
	face   *font.Face
	shaper shaping.HarfbuzzShaper

	// hbShape is the path of HarfBuzz's hb-shape, or "" to shape with go-text.
	// go-text selects the same glyphs as HarfBuzz but does not apply cursive
	// attachment offsets, which is what makes Nastaliq cascade and keeps its
	// dots in place; real HarfBuzz does.
	hbShape  string
	fontPath string // hb-shape reads the font from a file
}

// Shaper chooses the shaping engine.
type Shaper string

const (
	ShaperAuto     Shaper = "auto"     // HarfBuzz when hb-shape is on PATH, else go-text
	ShaperHarfBuzz Shaper = "harfbuzz" // HarfBuzz; an error if hb-shape is missing
	ShaperGo       Shaper = "go"       // go-text only (flat Nastaliq, some dots misplaced)
)

// New picks the shaper and loads the Nastaliq font at path, or when path is
// empty the embedded font that suits the shaper (Gulzar with HarfBuzz, Noto
// Nastaliq Urdu with go-text).
func New(path string, shaper Shaper) (*Renderer, error) {
	if shaper == "" {
		shaper = ShaperAuto
	}
	hb := ""
	switch shaper {
	case ShaperGo:
	case ShaperAuto, ShaperHarfBuzz:
		p, err := exec.LookPath("hb-shape")
		if err != nil && shaper == ShaperHarfBuzz {
			return nil, fmt.Errorf("faalimg: hb-shape not found (install harfbuzz): %w", err)
		}
		hb = p
	default:
		return nil, fmt.Errorf("faalimg: unknown shaper %q (auto, harfbuzz, go)", shaper)
	}

	data := notoFont
	if hb != "" {
		data = gulzarFont
	}
	if path != "" {
		b, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("faalimg: read font: %w", err)
		}
		data = b
	}
	face, err := font.ParseTTF(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("faalimg: parse font: %w", err)
	}
	r := &Renderer{face: face}
	if hb != "" {
		if path == "" {
			// hb-shape needs a file: write the embedded font once.
			f, err := os.CreateTemp("", "hooshang-nastaliq-*.ttf")
			if err != nil {
				return nil, err
			}
			if _, err := f.Write(data); err != nil {
				f.Close()
				return nil, err
			}
			f.Close()
			path = f.Name()
		}
		r.hbShape, r.fontPath = hb, path
	}
	return r, nil
}

// Engine reports which shaper is in use: "harfbuzz" or "go".
func (r *Renderer) Engine() string {
	if r.hbShape != "" {
		return "harfbuzz"
	}
	return "go"
}

// hbUnits is the font size hb-shape is run at; positions are scaled from it.
const hbUnits = 1000

// normalize writes characters some Nastaliq fonts lack in an equivalent
// form: «ۀ» (U+06C0) as «هٔ» (heh + U+0654 hamza above).
var normalize = strings.NewReplacer("\u06C0", "\u0647\u0654")

// digitsLTR reverses every run of digits (with the separators inside it), so
// that numbers read left to right when the line is shaped right to left. This
// is the part of the bidi algorithm a poem line needs; digits don't join, so
// reversing them doesn't change their shapes.
func digitsLTR(text string) string {
	rs := []rune(text)
	isDigit := func(r rune) bool { return unicode.IsDigit(r) }
	isSep := func(r rune) bool { return r == '.' || r == ',' || r == '/' || r == ':' || r == '٫' || r == '٬' }
	for i := 0; i < len(rs); {
		if !isDigit(rs[i]) {
			i++
			continue
		}
		j := i
		for j < len(rs) && (isDigit(rs[j]) || (isSep(rs[j]) && j+1 < len(rs) && isDigit(rs[j+1]))) {
			j++
		}
		for a, b := i, j-1; a < b; a, b = a+1, b-1 {
			rs[a], rs[b] = rs[b], rs[a]
		}
		i = j
	}
	return string(rs)
}

// shape shapes text right to left at size pixels.
func (r *Renderer) shape(text string, size int) (shaping.Output, error) {
	text = digitsLTR(normalize.Replace(text))
	// go-text gives the line metrics; with HarfBuzz its glyphs are replaced,
	// so only HarfBuzz's output decides whether a glyph is missing.
	out, err := r.goShape(text, size, r.hbShape == "")
	if err != nil || r.hbShape == "" {
		return out, err
	}
	// Same glyphs, HarfBuzz's positions.
	glyphs, err := r.harfbuzz(text)
	if err != nil {
		return out, err
	}
	scale := func(v int) fixed.Int26_6 { return fixed.Int26_6(int64(v) * int64(size) * 64 / hbUnits) }
	out.Glyphs = out.Glyphs[:0]
	var adv fixed.Int26_6
	for _, g := range glyphs {
		gl := shaping.Glyph{GlyphID: font.GID(g.G), XOffset: scale(g.Dx), YOffset: scale(g.Dy), Advance: scale(g.Ax)}
		adv += gl.Advance
		out.Glyphs = append(out.Glyphs, gl)
	}
	out.Advance = adv
	return out, nil
}

type hbGlyph struct {
	G  int `json:"g"`
	Dx int `json:"dx"`
	Dy int `json:"dy"`
	Ax int `json:"ax"`
}

// harfbuzz runs hb-shape on one line (no shell; text is an argument).
func (r *Renderer) harfbuzz(text string) ([]hbGlyph, error) {
	cmd := exec.Command(r.hbShape, "--font-file", r.fontPath, "--font-size", strconv.Itoa(hbUnits),
		"--direction", "rtl", "--script", "Arab", "--language", "fa",
		"--no-glyph-names", "--output-format", "json", "--", text)
	b, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("faalimg: hb-shape: %w", err)
	}
	var gs []hbGlyph
	if err := json.Unmarshal(bytes.TrimSpace(b), &gs); err != nil {
		return nil, fmt.Errorf("faalimg: hb-shape output: %w", err)
	}
	for _, g := range gs {
		if g.G == 0 {
			return nil, fmt.Errorf("%w: %q", ErrMissingGlyphs, text)
		}
	}
	return gs, nil
}

func (r *Renderer) goShape(text string, size int, check bool) (shaping.Output, error) {
	runes := []rune(text)
	out := r.shaper.Shape(shaping.Input{
		Text:      runes,
		RunStart:  0,
		RunEnd:    len(runes),
		Direction: di.DirectionRTL,
		Face:      r.face,
		Size:      fixed.I(size),
		Script:    language.Arabic,
		Language:  language.NewLanguage("fa"),
	})
	for _, g := range out.Glyphs {
		if check && g.GlyphID == 0 {
			return out, fmt.Errorf("%w: %q", ErrMissingGlyphs, text)
		}
	}
	return out, nil
}

// Render draws the title and the hemistichs (in order: first and second of
// each بیت) and returns a PNG.
func (r *Renderer) Render(title string, hemistichs []string) ([]byte, error) {
	if len(hemistichs) == 0 {
		return nil, errors.New("faalimg: no text")
	}
	r.mu.Lock()
	defer r.mu.Unlock()

	// Pick the largest size at which every hemistich fits its column.
	colW := (width - 2*margin - gutter) / 2
	widest := 0
	for _, h := range hemistichs {
		out, err := r.shape(h, refSize)
		if err != nil {
			return nil, err
		}
		widest = max(widest, out.Advance.Ceil())
	}
	size := maxSize
	if widest > 0 {
		size = min(maxSize, colW*refSize/widest)
	}
	if size < minSize {
		size = minSize // a very long hemistich overflows a little rather than becoming unreadable
	}

	// Shape everything at the final sizes.
	titleOut, err := r.shape(title, titleSize)
	if err != nil {
		return nil, err
	}
	lines := make([]shaping.Output, len(hemistichs))
	for i, h := range hemistichs {
		if lines[i], err = r.shape(h, size); err != nil {
			return nil, err
		}
	}
	// Nastaliq lines are tall: the font's line bounds already include the
	// cascade; add a little air between rows.
	ascent := lines[0].LineBounds.Ascent.Ceil()
	descent := -lines[0].LineBounds.Descent.Floor()
	rowH := (ascent + descent) * 105 / 100
	titleH := titleOut.LineBounds.Ascent.Ceil() - titleOut.LineBounds.Descent.Floor()

	rows := (len(lines) + 1) / 2
	top := margin
	sepY := top + titleH + 24
	bodyTop := sepY + 30
	height := bodyTop + rows*rowH + margin

	img := image.NewRGBA(image.Rect(0, 0, width, height))
	draw.Draw(img, img.Bounds(), &image.Uniform{paper}, image.Point{}, draw.Src)
	drawFrame(img)

	rdr := &render.Renderer{Color: accent, PixScale: 1}
	rdr.FontSize = titleSize
	rdr.DrawShapedRunAt(titleOut, img, (width-titleOut.Advance.Ceil())/2, top+titleOut.LineBounds.Ascent.Ceil())
	drawRule(img, sepY)

	rdr.Color, rdr.FontSize = ink, float32(size)
	right := [2]int{width - margin - colW, margin} // x of the right and the left column
	for i, out := range lines {
		row, col := i/2, i%2
		x := right[col] + (colW-out.Advance.Ceil())/2
		y := bodyTop + row*rowH + ascent
		rdr.DrawShapedRunAt(out, img, x, y)
	}

	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// drawFrame draws a double border.
func drawFrame(img *image.RGBA) {
	b := img.Bounds()
	for _, inset := range []struct{ d, w int }{{24, 4}, {36, 1}} {
		r := image.Rect(b.Min.X+inset.d, b.Min.Y+inset.d, b.Max.X-inset.d, b.Max.Y-inset.d)
		fill(img, image.Rect(r.Min.X, r.Min.Y, r.Max.X, r.Min.Y+inset.w))
		fill(img, image.Rect(r.Min.X, r.Max.Y-inset.w, r.Max.X, r.Max.Y))
		fill(img, image.Rect(r.Min.X, r.Min.Y, r.Min.X+inset.w, r.Max.Y))
		fill(img, image.Rect(r.Max.X-inset.w, r.Min.Y, r.Max.X, r.Max.Y))
	}
}

// drawRule draws a short centered rule under the title.
func drawRule(img *image.RGBA, y int) {
	fill(img, image.Rect(width/2-220, y, width/2+220, y+2))
	fill(img, image.Rect(width/2-6, y-5, width/2+6, y+7))
}

func fill(img *image.RGBA, r image.Rectangle) {
	draw.Draw(img, r, &image.Uniform{frame}, image.Point{}, draw.Src)
}
