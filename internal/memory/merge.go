package memory

import (
	"strings"
	"time"

	"github.com/ehsanranjbar/hooshang/internal/store"
)

// Extracted is what an extraction returns: a nickname and summary (members
// only) and items with the messages they rest on.
type Extracted struct {
	Nickname string
	Summary  string
	Items    []ExtractedItem
}

// ExtractedItem is one item the model extracted.
type ExtractedItem struct {
	Kind string
	Text string
	Msgs []int64 // the message ids it rests on (may be empty)
}

// maxSource is how many message ids an item keeps.
const maxSource = 5

// staleAfter is how long a once-seen, unlocked item is kept.
const staleAfter = 30 * 24 * time.Hour

// Merge folds an extraction into the current memory, in place:
//   - kinds the extractor may not write, and suppressed texts, are dropped;
//   - an extracted item matching a stored one (same kind, same folded text)
//     is a re-observation only when it rests on a message the stored item
//     did not (the model echoing the current list proves nothing): then
//     Seen++, LastAt = now, the sources merge, an expiry is pushed back;
//   - a new item starts with Seen 1 and the kind's TTL;
//   - stored items the extraction left out are kept; once-seen unlocked items
//     not observed for 30 days are dropped;
//   - each kind is capped at maxItems: locked first, then most seen, then most recent.
//
// Nickname and summary keep an admin-locked value; an empty or suppressed
// extracted value keeps the current one.
func Merge(m *store.Memory, ext Extracted, kinds []string, now time.Time, maxItems int) {
	allowed := map[string]bool{}
	for _, k := range kinds {
		allowed[k] = Policies[k].Extract
	}
	drop := map[string]bool{}
	for _, s := range m.Suppressed {
		if n := Normalize(s); n != "" {
			drop[n] = true
		}
	}
	m.Nickname, m.NicknameLocked = mergeString(m.Nickname, m.NicknameLocked, ext.Nickname, drop)
	m.Summary, m.SummaryLocked = mergeString(m.Summary, m.SummaryLocked, ext.Summary, drop)

	for _, x := range ext.Items {
		kind, textT := strings.TrimSpace(x.Kind), strings.TrimSpace(x.Text)
		if textT == "" || !allowed[kind] || drop[Normalize(textT)] {
			continue
		}
		if cur := find(m, kind, textT); cur != nil {
			if observedAgain(cur, x.Msgs, now) {
				cur.Seen++
				cur.LastAt = now
				cur.Source = mergeSource(cur.Source, x.Msgs)
				if ttl := Policies[kind].TTL; ttl > 0 {
					cur.Expires = now.Add(ttl)
				}
			}
			continue
		}
		it := store.Item{Kind: kind, Text: textT, Seen: 1, FirstAt: now, LastAt: now, Source: mergeSource(nil, x.Msgs)}
		if ttl := Policies[kind].TTL; ttl > 0 {
			it.Expires = now.Add(ttl)
		}
		m.Add(it)
	}

	kept := m.Items[:0]
	for _, it := range m.Items {
		if !it.Locked && it.Seen <= 1 && allowed[it.Kind] && now.Sub(it.LastAt) > staleAfter {
			continue
		}
		kept = append(kept, it)
	}
	m.Items = kept
	capKinds(m, kinds, maxItems)
	m.UpdatedAt = now
}

// find returns the stored item of the kind with the same folded text, or nil.
func find(m *store.Memory, kind, textT string) *store.Item {
	n := Normalize(textT)
	for i := range m.Items {
		if m.Items[i].Kind == kind && Normalize(m.Items[i].Text) == n {
			return &m.Items[i]
		}
	}
	return nil
}

// observedAgain reports whether an extraction counts as a new observation of
// a stored item: it cites a message the item did not rest on, or, when it
// cites nothing, the item was last seen more than a day ago.
func observedAgain(cur *store.Item, msgs []int64, now time.Time) bool {
	if len(msgs) == 0 {
		return now.Sub(cur.LastAt) > 24*time.Hour
	}
	have := map[int64]bool{}
	for _, id := range cur.Source {
		have[id] = true
	}
	for _, id := range msgs {
		if id != 0 && !have[id] {
			return true
		}
	}
	return false
}

// mergeSource appends new ids, keeping the latest maxSource.
func mergeSource(cur, add []int64) []int64 {
	have := map[int64]bool{}
	for _, id := range cur {
		have[id] = true
	}
	out := append([]int64(nil), cur...)
	for _, id := range add {
		if id != 0 && !have[id] {
			have[id] = true
			out = append(out, id)
		}
	}
	if len(out) > maxSource {
		out = out[len(out)-maxSource:]
	}
	return out
}

// capKinds keeps at most max items per kind in kinds (locked first, then the
// most seen, then the most recent); other kinds are untouched.
func capKinds(m *store.Memory, kinds []string, max int) {
	if max <= 0 {
		return
	}
	for _, kind := range kinds {
		items := m.Kind(kind)
		if len(items) <= max {
			continue
		}
		store.SortItems(items)
		for _, it := range items[max:] {
			m.Remove(it.ID)
		}
	}
}

// mergeString merges a single-valued field (nickname, summary).
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
