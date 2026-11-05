package ganjoor

import (
	"strings"
)

// Format renders a poem for sending: the first line is "📜 {FullTitle}",
// then a blank line, then couplets as "hemistich1\nhemistich2" separated by
// a blank line. With maxCouplets > 0 the output is cut to that many couplets
// and "…" is appended.
func Format(p *Poem, maxCouplets int) string {
	var b strings.Builder
	if p.FullTitle != "" {
		b.WriteString("📜 ")
		b.WriteString(p.FullTitle)
		b.WriteByte('\n')
	}
	b.WriteByte('\n')

	couplets := 0
	for i := 0; i < len(p.Hemistichs); i += 2 {
		if maxCouplets > 0 && couplets >= maxCouplets {
			b.WriteString("\n\n…")
			return b.String()
		}
		if couplets > 0 {
			b.WriteByte('\n')
			b.WriteByte('\n')
		}
		couplets++
		first := p.Hemistichs[i]
		second := ""
		if i+1 < len(p.Hemistichs) {
			second = p.Hemistichs[i+1]
		}
		b.WriteString(first)
		if second != "" {
			b.WriteByte('\n')
			b.WriteString(second)
		}
	}
	return b.String()
}

// Excerpt renders the first maxCouplets couplets (بیت) of a poem with no
// header: "hemistich1\nhemistich2" per couplet, a blank line between couplets.
func Excerpt(p *Poem, maxCouplets int) string {
	var couplets []string
	for i := 0; i < len(p.Hemistichs) && len(couplets) < maxCouplets; i += 2 {
		c := p.Hemistichs[i]
		if i+1 < len(p.Hemistichs) {
			c += "\n" + p.Hemistichs[i+1]
		}
		couplets = append(couplets, c)
	}
	return strings.Join(couplets, "\n\n")
}
