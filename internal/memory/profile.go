// Package memory renders, merges and updates the per-group member profiles.
package memory

import (
	"strings"
	"unicode"

	"github.com/ehsanranjbar/hooshang/internal/store"
)

// sectionTitles are the Persian titles used by Render.
var sectionTitles = map[string]string{
	"nickname":      "لقب",
	"summary":       "خلاصه",
	"facts":         "دانسته‌ها",
	"interests":     "علاقه‌مندی‌ها",
	"quirks":        "عادت‌ها",
	"relationships": "رابطه‌ها",
	"boundaries":    "مرزها",
	"orders":        "دستورهای ادمین",
}

// Sections are the profile sections in display order.
func Sections() []string {
	return []string{"nickname", "summary", "facts", "interests", "quirks", "relationships", "boundaries", "orders"}
}

// Normalize folds Persian text for comparison: it removes ZWNJs and spaces,
// folds ي→ی and ك→ک, and lowercases.
func Normalize(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch r {
		case '\u200C', ' ':
			continue
		case 'ي':
			b.WriteRune('ی')
		case 'ك':
			b.WriteRune('ک')
		default:
			b.WriteRune(unicode.ToLower(r))
		}
	}
	return b.String()
}

// itemsOf returns the item slice of a section (or nil for nickname/summary/
// unknown sections).
func itemsOf(p *store.Profile, section string) *[]store.Item {
	switch section {
	case "facts":
		return &p.Facts
	case "interests":
		return &p.Interests
	case "quirks":
		return &p.Quirks
	case "relationships":
		return &p.Relationships
	case "boundaries":
		return &p.Boundaries
	case "orders":
		return &p.Orders
	}
	return nil
}

// RenderOrders returns the admin's orders about a member, joined with «؛ »
// (empty if none). They are not part of Render: prompts show them on their own,
// for everyone in the conversation, as rules to follow.
func RenderOrders(p store.Profile) string {
	var parts []string
	for _, it := range p.Orders {
		if t := strings.TrimSpace(it.Text); t != "" {
			parts = append(parts, t)
		}
	}
	return strings.Join(parts, "؛ ")
}

// Render returns a compact Persian text of the profile for prompts: one line
// per non-empty section. Suppressed and Orders (see RenderOrders) are not
// rendered.
func Render(p store.Profile) string {
	var b strings.Builder
	write := func(label, value string) {
		if value == "" {
			return
		}
		if b.Len() > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(label)
		b.WriteString(": ")
		b.WriteString(value)
	}
	write("لقب", strings.TrimSpace(p.Nickname))
	write("خلاصه", strings.TrimSpace(p.Summary))
	for _, section := range []string{"facts", "interests", "quirks", "relationships", "boundaries"} {
		var parts []string
		for _, it := range *itemsOf(&p, section) {
			if t := strings.TrimSpace(it.Text); t != "" {
				parts = append(parts, t)
			}
		}
		if len(parts) > 0 {
			write(sectionTitles[section], strings.Join(parts, "؛ "))
		}
	}
	return b.String()
}
