package ganjoor

import (
	"github.com/ehsanranjbar/hooshang/internal/text"
	"strings"
	"unicode"
)

// poets are the Ganjoor poet IDs from §1.2.
var poets = []struct {
	id   int
	name string
}{
	{2, "حافظ"},
	{3, "خیام"},
	{4, "فردوسی"},
	{5, "مولوی"},
	{5, "مولانا"},
	{6, "نظامی"},
	{7, "سعدی"},
	{8, "پروین"},
	{9, "عطار"},
	{11, "وحشی"},
	{12, "رودکی"},
	{28, "باباطاهر"},
	{35, "شهریار"},
	{88, "ایرج"},
}

// aliases are other names that identify a poet on their own (not shown in
// the list).
var aliases = map[string]int{
	"رومی":          5,
	"اعتصامی":       8,
	"بافقی":         11,
	"باباطاهرعریان": 28,
	"ایرجمیرزا":     88,
}

// normalizePersian folds a poet name (text.Squash) so spelling variants match.
func normalizePersian(s string) string { return text.Squash(s) }

// PoetID resolves a poet name to its Ganjoor poet ID. Spelling variants,
// ZWNJs and spaces don't matter, and a longer form works when one of its words
// is a known name: «ایرج میرزا», «حافظ شیرازی», «از سعدی». ok is false for
// unknown names.
func PoetID(name string) (int, bool) {
	if id, ok := poetByName(normalizePersian(name)); ok {
		return id, true
	}
	for _, w := range strings.FieldsFunc(name, func(r rune) bool {
		return unicode.IsSpace(r) || r == '\u200C' || unicode.IsPunct(r)
	}) {
		if id, ok := poetByName(normalizePersian(w)); ok {
			return id, true
		}
	}
	return 0, false
}

func poetByName(n string) (int, bool) {
	if n == "" {
		return 0, false
	}
	for _, p := range poets {
		if n == normalizePersian(p.name) {
			return p.id, true
		}
	}
	if id, ok := aliases[n]; ok {
		return id, true
	}
	return 0, false
}

// PoetNames returns the poet display names (one entry per poet; مولوی and
// مولانا share a poet).
func PoetNames() []string {
	seen := make(map[int]bool)
	var names []string
	for _, p := range poets {
		if seen[p.id] {
			continue
		}
		seen[p.id] = true
		names = append(names, p.name)
	}
	return names
}
