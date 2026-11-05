package memory

import (
	"strings"

	"github.com/ehsanranjbar/hooshang/internal/store"
)

// Merge combines the current stored profile with the freshly extracted one:
// the result is the extracted profile, plus every Locked item of current
// (same section, same text, kept with Locked=true). Any item whose
// normalized text matches a Suppressed entry is dropped. Duplicates are
// removed and each section is capped at maxItems (locked items first).
// Suppressed is copied from current.
func Merge(current, extracted store.Profile, maxItems int) store.Profile {
	r := extracted
	r.Suppressed = current.Suppressed
	r.Orders = current.Orders // only the admin changes orders; the updater never sees them

	drop := make(map[string]bool)
	for _, s := range current.Suppressed {
		if n := Normalize(s); n != "" {
			drop[n] = true
		}
	}

	// Nickname and summary: an admin-set value is kept; an empty or
	// suppressed extracted value keeps the current one.
	r.Nickname, r.NicknameLocked = mergeString(current.Nickname, current.NicknameLocked, extracted.Nickname, drop)
	r.Summary, r.SummaryLocked = mergeString(current.Summary, current.SummaryLocked, extracted.Summary, drop)

	for _, section := range []string{"facts", "interests", "quirks", "relationships", "boundaries"} {
		dst := itemsOf(&r, section)
		src := itemsOf(&current, section)
		items := append([]store.Item(nil), *dst...) // never write into the caller's slice
		// Re-attach locked items from current that the extractor lost, and keep
		// the lock on the ones it reproduced (it never sees the flag).
		locked := map[string]bool{}
		for _, it := range *src {
			if it.Locked {
				locked[Normalize(it.Text)] = true
				if !containsItem(items, it.Text) {
					items = append(items, store.Item{Text: it.Text, Locked: true})
				}
			}
		}
		for i := range items {
			if locked[Normalize(items[i].Text)] {
				items[i].Locked = true
			}
		}
		*dst = dedupCap(items, drop, maxItems)
	}
	return r
}

// containsItem reports whether items contains an item with the same
// normalized text.
func containsItem(items []store.Item, text string) bool {
	n := Normalize(text)
	for _, it := range items {
		if Normalize(it.Text) == n {
			return true
		}
	}
	return false
}

// dedupCap drops suppressed and duplicate items (first occurrence wins,
// locked first) and caps the result at max (< 1 means uncapped).
func dedupCap(items []store.Item, drop map[string]bool, max int) []store.Item {
	// Locked items come first so the cap keeps them.
	locked := make([]store.Item, 0, len(items))
	rest := make([]store.Item, 0, len(items))
	for _, it := range items {
		if it.Locked {
			locked = append(locked, it)
		} else {
			rest = append(rest, it)
		}
	}
	var out []store.Item
	seen := make(map[string]bool)
	for _, group := range [][]store.Item{locked, rest} {
		for _, it := range group {
			text := strings.TrimSpace(it.Text)
			if text == "" {
				continue
			}
			n := Normalize(text)
			if drop[n] || seen[n] {
				continue
			}
			seen[n] = true
			out = append(out, store.Item{Text: text, Locked: it.Locked})
		}
	}
	if max > 0 && len(out) > max {
		out = out[:max]
	}
	return out
}

// mergeString merges a single-valued section (nickname, summary).
func mergeString(cur string, locked bool, ext string, drop map[string]bool) (string, bool) {
	ext = strings.TrimSpace(ext)
	if locked {
		return cur, true
	}
	if ext == "" || drop[Normalize(ext)] {
		if drop[Normalize(cur)] {
			return "", false
		}
		return cur, false
	}
	return ext, false
}
