// Package text folds Persian text for comparison. It is the one place that
// decides what "the same word" means: name matching, glossary terms, memory
// dedup and poet names all go through it.
package text

import (
	"strings"
	"unicode"
)

// Fold normalizes s for matching: lowercase, Arabic ي/ك/ة folded to the
// Persian ی/ک/ه, tatweel and diacritics dropped, ZWNJ and direction marks
// turned into spaces, runs of whitespace collapsed to one space, and the
// result trimmed.
func Fold(s string) string {
	var b strings.Builder
	space := false
	for _, r := range s {
		switch {
		case r == 'ي':
			r = 'ی'
		case r == 'ك':
			r = 'ک'
		case r == 'ة':
			r = 'ه'
		case r == 'ـ' || (r >= 'ً' && r <= 'ْ') || r == 'ٰ':
			continue // tatweel, diacritics
		case r == '‌' || r == '‎' || r == '‏' || unicode.IsSpace(r):
			space = true
			continue
		}
		if space && b.Len() > 0 {
			b.WriteByte(' ')
		}
		space = false
		b.WriteRune(unicode.ToLower(r))
	}
	return b.String()
}

// Squash is Fold without any spaces, for things written with or without
// them («ایرج میرزا» / «ایرجمیرزا», «به روز» / «به‌روز»).
func Squash(s string) string {
	return strings.ReplaceAll(Fold(s), " ", "")
}

// FoldKeepLen lowercases and folds ي→ی and ك→ک only, so the result has the
// same runes as s at the same positions: for whole-word matching that needs
// to look at the neighbours of a match.
func FoldKeepLen(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch r {
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
