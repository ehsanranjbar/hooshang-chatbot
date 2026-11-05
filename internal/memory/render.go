package memory

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/ehsanranjbar/hooshang/internal/i18n"
	"github.com/ehsanranjbar/hooshang/internal/store"
)

// Render returns a compact Persian text of a member's memory for prompts and
// /me: nickname, summary, then one line per kind in RenderKinds. Episodes
// carry their date. Orders (see RenderOrders), suppressed texts and the
// group/self kinds are left out.
func Render(m store.Memory) string {
	return RenderItems(m, m.Items, time.Now())
}

// RenderItems renders a memory with a chosen subset of its items (RAG picks
// the relevant ones when a memory is too big for its share of the prompt).
func RenderItems(m store.Memory, items []store.Item, now time.Time) string {
	var b strings.Builder
	write := func(label, value string) {
		if value == "" {
			return
		}
		if b.Len() > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(label + ": " + value)
	}
	write(Title("nickname"), strings.TrimSpace(m.Nickname))
	write(Title("summary"), strings.TrimSpace(m.Summary))
	write(Title("analysis"), strings.TrimSpace(m.Analysis))
	for _, kind := range RenderKinds {
		var parts []string
		for _, it := range items {
			if it.Kind != kind {
				continue
			}
			t := strings.TrimSpace(it.Text)
			if t == "" {
				continue
			}
			if kind == store.KindEpisode {
				t = "(" + i18n.When(it.LastAt, now) + ") " + t
			}
			parts = append(parts, t)
		}
		if len(parts) > 0 {
			write(Title(kind), strings.Join(parts, "؛ "))
		}
	}
	return b.String()
}

// RenderKind joins the texts of one kind with «؛ » (empty if none).
func RenderKind(m store.Memory, kind string) string {
	var parts []string
	for _, it := range m.Kind(kind) {
		if t := strings.TrimSpace(it.Text); t != "" {
			parts = append(parts, t)
		}
	}
	return strings.Join(parts, "؛ ")
}

// RenderOrders returns the admin's standing orders about a member.
func RenderOrders(m store.Memory) string { return RenderKind(m, store.KindOrder) }

// RenderSelf renders what the bot has said before (stances, running jokes,
// promises) as bullet lines, promises with their date.
func RenderSelf(m store.Memory, now time.Time) string {
	var lines []string
	for _, kind := range SelfKinds {
		for _, it := range m.Kind(kind) {
			t := strings.TrimSpace(it.Text)
			if t == "" {
				continue
			}
			if kind == store.KindPromise {
				t = "(" + i18n.When(it.LastAt, now) + ") " + t
			}
			lines = append(lines, "- "+Title(kind)+": "+t)
		}
	}
	return strings.Join(lines, "\n")
}

// RenderEvents renders the top n events by score, most recent first:
// "- (دیروز) …".
func RenderEvents(m store.Memory, n int, now time.Time) string {
	events := m.Kind(store.KindEvent)
	if n <= 0 || len(events) == 0 {
		return ""
	}
	sort.SliceStable(events, func(i, j int) bool { return events[i].Score > events[j].Score })
	if len(events) > n {
		events = events[:n]
	}
	sort.SliceStable(events, func(i, j int) bool { return events[i].LastAt.After(events[j].LastAt) })
	var lines []string
	for _, e := range events {
		lines = append(lines, "- ("+i18n.When(e.LastAt, now)+") "+e.Text)
	}
	return strings.Join(lines, "\n")
}

// RenderSamples renders the group's style samples as quoted lines.
func RenderSamples(m store.Memory) string {
	var lines []string
	for _, it := range m.Kind(store.KindSample) {
		if t := strings.TrimSpace(it.Text); t != "" {
			lines = append(lines, "«"+t+"»")
		}
	}
	return strings.Join(lines, "\n")
}

// RenderStyle sums up how a member writes in one Persian line, from the
// counted stats; "" when there is too little to say (under 20 messages).
func RenderStyle(s store.Style, names map[int64]string) string {
	if s.Msgs < 20 {
		return ""
	}
	var parts []string
	avg := float64(s.Words) / float64(s.Msgs)
	switch {
	case avg < 4:
		parts = append(parts, i18n.StyleShort)
	case avg > 15:
		parts = append(parts, i18n.StyleLong)
	}
	if float64(s.Emoji)/float64(s.Msgs) > 0.4 {
		parts = append(parts, i18n.StyleEmoji)
	}
	night, morning := 0, 0
	for h, n := range s.Hours {
		switch {
		case h >= 0 && h < 5:
			night += n
		case h >= 6 && h < 11:
			morning += n
		}
	}
	switch {
	case float64(night)/float64(s.Msgs) > 0.3:
		parts = append(parts, i18n.StyleNight)
	case float64(morning)/float64(s.Msgs) > 0.4:
		parts = append(parts, i18n.StyleMorning)
	}
	if laugh := topKey(s.Laughs); laugh != "" && s.Laughs[laugh] >= 5 {
		parts = append(parts, fmt.Sprintf(i18n.StyleLaughs, laugh))
	}
	if who := topReply(s.ReplyTo); who != 0 && s.ReplyTo[who] >= 5 && names[who] != "" {
		parts = append(parts, fmt.Sprintf(i18n.StyleTalksTo, names[who]))
	}
	return strings.Join(parts, "، ")
}

// sortByCount orders keys by their count, highest first (ties by key).
func sortByCount(keys []string, counts map[string]int) {
	sort.Slice(keys, func(i, j int) bool {
		if counts[keys[i]] != counts[keys[j]] {
			return counts[keys[i]] > counts[keys[j]]
		}
		return keys[i] < keys[j]
	})
}

// topKey returns the key with the highest count ("" when empty).
func topKey(m map[string]int) string {
	best, n := "", 0
	for k, v := range m {
		if v > n || (v == n && k < best) {
			best, n = k, v
		}
	}
	return best
}

// topReply returns the user replied to most (0 when none).
func topReply(m map[int64]int) int64 {
	var best int64
	n := 0
	for k, v := range m {
		if v > n || (v == n && k < best) {
			best, n = k, v
		}
	}
	return best
}
