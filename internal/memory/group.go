package memory

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/ehsanranjbar/hooshang/internal/i18n"
	"github.com/ehsanranjbar/hooshang/internal/llm"
	"github.com/ehsanranjbar/hooshang/internal/prompts"
	"github.com/ehsanranjbar/hooshang/internal/store"
	"github.com/ehsanranjbar/hooshang/internal/text"
)

// GroupConfig configures the group memory: a short list of the topics and
// events the group cared about, each forgotten TTL after it was last seen,
// plus a handful of anonymised sample messages that show how the group
// writes. A zero TTL disables it.
type GroupConfig struct {
	TTL             time.Duration
	MaxItems        int // events kept per group, by engagement
	ContextMessages int // messages read per update; 0 → 200
	Samples         int // style samples kept; 0 = none
}

// minGroupMessages is how many new member messages an update needs.
const minGroupMessages = 10

// maxNewEvents is how many events one update may add.
const maxNewEvents = 5

// EnqueueGroup queues a group memory update for the chat (deduplicated).
func (u *Updater) EnqueueGroup(chatID int64) {
	if u.cfg.Group.TTL > 0 {
		u.Enqueue(chatID, store.GroupEntity)
	}
}

// groupExtracted is the model's JSON.
type groupExtracted struct {
	Events *[]struct {
		Text   string  `json:"text"`
		Msgs   []int64 `json:"msgs"`
		SameAs uint64  `json:"same_as"`
	} `json:"events"`
}

// UpdateGroupNow updates the group memory from the messages since the last
// update: the events (scored by the engagement of the messages the model says
// they rest on, so the ranking rests on what the group did) and the style
// samples.
func (u *Updater) UpdateGroupNow(ctx context.Context, chatID int64) error {
	g := u.cfg.Group
	chat, err := u.db.GetChat(chatID)
	if err != nil {
		return err
	}
	n := g.ContextMessages
	if n <= 0 {
		n = 200
	}
	all, err := u.db.RecentMessages(chatID, n)
	if err != nil {
		return err
	}
	var msgs []store.Message
	for _, m := range all {
		if !m.FromBot && m.Date.After(chat.GroupUpdatedAt) {
			msgs = append(msgs, m)
		}
	}
	if len(msgs) < minGroupMessages {
		return u.db.UpdateChat(chatID, func(c *store.Chat) { c.MsgsSinceGroupUpdate = 0 })
	}
	done := func() error {
		return u.db.UpdateChat(chatID, func(c *store.Chat) {
			c.MsgsSinceGroupUpdate = 0
			c.GroupUpdatedAt = time.Now()
		})
	}
	if g.Samples > 0 {
		samples := Samples(all, u.names(chatID), g.Samples)
		if _, err := u.db.UpdateMemory(chatID, store.GroupEntity, func(m *store.Memory) { replaceKind(m, store.KindSample, samples, time.Now()) }); err != nil {
			return err
		}
	}

	cur, err := u.db.GetMemory(chatID, store.GroupEntity)
	if err != nil {
		return err
	}
	var evLines, lines []string
	for _, e := range cur.Kind(store.KindEvent) {
		evLines = append(evLines, fmt.Sprintf("%d: %s", e.ID, e.Text))
	}
	engagement := map[int64]int{}
	for _, m := range msgs {
		engagement[m.ID] = m.Engagement()
		line := fmt.Sprintf("[%d] %s: %s", m.ID, m.Name, m.Text)
		if e := m.Engagement(); e > 0 {
			line += fmt.Sprintf(" {+%d}", e)
		}
		lines = append(lines, line)
	}
	prompt, err := prompts.GroupExtract(prompts.GroupData{
		Language: u.cfg.Language, Events: strings.Join(evLines, "\n"), Transcript: strings.Join(lines, "\n"), Max: maxNewEvents,
	})
	if err != nil {
		return err
	}
	resp, err := u.c.Chat(ctx, llm.Request{
		Model: u.model, Messages: []llm.Message{{Role: "user", Content: prompt}}, Temperature: 0.2, MaxTokens: max(u.cfg.MaxTokens, 2048),
	})
	if err != nil {
		u.logger.Warn("group memory: llm call failed", "chat", chatID, "err", err)
		return u.db.UpdateChat(chatID, func(c *store.Chat) { c.MsgsSinceGroupUpdate = 0 })
	}
	raw, err := llm.ExtractJSON(resp.Content)
	var ext groupExtracted
	if err == nil {
		err = json.Unmarshal(raw, &ext)
	}
	if err == nil && ext.Events == nil {
		err = fmt.Errorf(`no "events" key`)
	}
	if err != nil {
		u.logger.Warn("group memory: invalid output, keeping the old events", "chat", chatID, "err", err)
		return done()
	}

	now := time.Now()
	mem, err := u.db.UpdateMemory(chatID, store.GroupEntity, func(m *store.Memory) {
		added := 0
		for _, x := range *ext.Events {
			textT := strings.TrimSpace(x.Text)
			if textT == "" {
				continue
			}
			score := 1
			for _, id := range x.Msgs {
				score += engagement[id] // ids the model made up count as 0
			}
			cur := m.Find(x.SameAs)
			if cur == nil || cur.Kind != store.KindEvent {
				cur = find(m, store.KindEvent, textT)
			}
			if cur != nil {
				cur.Text, cur.LastAt, cur.Expires = textT, now, now.Add(g.TTL)
				cur.Score += score
				cur.Seen++
				cur.Source = mergeSource(cur.Source, x.Msgs)
				continue
			}
			if added == maxNewEvents {
				continue
			}
			added++
			m.Add(store.Item{Kind: store.KindEvent, Text: textT, Score: score, Seen: 1, FirstAt: now, LastAt: now,
				Expires: now.Add(g.TTL), Source: mergeSource(nil, x.Msgs)})
		}
		capEvents(m, g.MaxItems)
		m.UpdatedAt = now
	})
	if err != nil {
		return err
	}
	u.embed(chatID, store.GroupEntity, mem)
	return done()
}

// capEvents keeps the max highest-scoring events.
func capEvents(m *store.Memory, max int) {
	events := m.Kind(store.KindEvent)
	if max <= 0 || len(events) <= max {
		return
	}
	for i := range events {
		for j := i + 1; j < len(events); j++ {
			if events[j].Score > events[i].Score || (events[j].Score == events[i].Score && events[j].LastAt.After(events[i].LastAt)) {
				events[i], events[j] = events[j], events[i]
			}
		}
	}
	for _, e := range events[max:] {
		m.Remove(e.ID)
	}
}

// replaceKind replaces every item of a kind with the given texts.
func replaceKind(m *store.Memory, kind string, texts []string, now time.Time) {
	for _, it := range m.Kind(kind) {
		m.Remove(it.ID)
	}
	for _, t := range texts {
		m.Add(store.Item{Kind: kind, Text: t, Seen: 1, FirstAt: now, LastAt: now})
	}
}

// names maps the chat's member ids to their first names, for anonymising.
func (u *Updater) names(chatID int64) []string {
	members, err := u.db.Members(chatID)
	if err != nil {
		return nil
	}
	var out []string
	for _, m := range members {
		if m.FirstName != "" {
			out = append(out, m.FirstName)
		}
	}
	return out
}

// Samples picks up to n recent human messages of 3–15 words as examples of
// how the group writes, newest first, with the members' first names replaced
// by «فلانی» and media tags, commands and links left out.
func Samples(msgs []store.Message, names []string, n int) []string {
	var out []string
	seen := map[string]bool{}
	for i := len(msgs) - 1; i >= 0 && len(out) < n; i-- {
		m := msgs[i]
		t := strings.TrimSpace(m.Text)
		if m.FromBot || t == "" || strings.HasPrefix(t, "[") || strings.HasPrefix(t, "/") || strings.Contains(t, "http") {
			continue
		}
		words := strings.Fields(t)
		if len(words) < 3 || len(words) > 15 {
			continue
		}
		for _, name := range names {
			if len([]rune(name)) >= 3 {
				t = replaceWord(t, name, i18n.Someone)
			}
		}
		if f := text.Fold(t); seen[f] {
			continue
		} else {
			seen[f] = true
		}
		out = append(out, t)
	}
	return out
}

// replaceWord replaces whole-word occurrences of name in t.
func replaceWord(t, name, with string) string {
	fields := strings.Fields(t)
	fn := text.Fold(name)
	for i, w := range fields {
		if text.Fold(strings.Trim(w, "،,.!?؟:")) == fn {
			fields[i] = with
		}
	}
	return strings.Join(fields, " ")
}
