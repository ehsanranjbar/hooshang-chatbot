package memory

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/ehsanranjbar/hooshang/internal/i18n"
	"github.com/ehsanranjbar/hooshang/internal/llm"
	"github.com/ehsanranjbar/hooshang/internal/prompts"
	"github.com/ehsanranjbar/hooshang/internal/store"
)

// GroupConfig configures the group memory: a short list of the topics and
// events the group cared about, each forgotten TTL after it was last seen.
// A zero TTL disables it.
type GroupConfig struct {
	TTL             time.Duration
	MaxItems        int // kept per group, by engagement
	ContextMessages int // messages read per update; 0 → 200
}

// minGroupMessages is how many new member messages an update needs.
const minGroupMessages = 10

// maxNewEvents is how many events one update may add.
const maxNewEvents = 5

// EnqueueGroup queues a group memory update for the chat (deduplicated).
func (u *Updater) EnqueueGroup(chatID int64) {
	if u.cfg.Group.TTL > 0 {
		u.Enqueue(chatID, 0) // user 0 is the group itself
	}
}

// groupExtracted is the model's JSON.
type groupExtracted struct {
	Events *[]struct {
		Text   string  `json:"text"`
		Msgs   []int64 `json:"msgs"`
		SameAs int64   `json:"same_as"`
	} `json:"events"`
}

// UpdateGroupNow updates the group memory from the messages since the last
// update. The score of an event is the engagement (reactions and replies) of
// the messages the model says it is based on, so the ranking rests on what the
// group did, not on the model's opinion.
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

	events, err := u.db.GroupEvents(chatID, g.TTL)
	if err != nil {
		return err
	}
	var evLines, lines []string
	for _, e := range events {
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
		Language:   u.cfg.Language,
		Events:     strings.Join(evLines, "\n"),
		Transcript: strings.Join(lines, "\n"),
		Max:        maxNewEvents,
	})
	if err != nil {
		return err
	}
	maxTokens := u.cfg.MaxTokens
	if maxTokens < 2048 {
		maxTokens = 2048
	}
	resp, err := u.c.Chat(ctx, llm.Request{
		Model:       u.model,
		Messages:    []llm.Message{{Role: "user", Content: prompt}},
		Temperature: 0.2,
		MaxTokens:   maxTokens,
	})
	done := func() error {
		return u.db.UpdateChat(chatID, func(c *store.Chat) {
			c.MsgsSinceGroupUpdate = 0
			c.GroupUpdatedAt = time.Now()
		})
	}
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
	byID := map[int64]int{}
	for i, e := range events {
		byID[e.ID] = i
	}
	added := 0
	for _, x := range *ext.Events {
		text := strings.TrimSpace(x.Text)
		if text == "" {
			continue
		}
		score := 1
		for _, id := range x.Msgs {
			score += engagement[id] // ids the model made up count as 0
		}
		i, ok := byID[x.SameAs]
		if !ok {
			i, ok = sameText(events, text)
		}
		if ok {
			events[i].Text, events[i].LastAt = text, now
			events[i].Score += score
			continue
		}
		if added == maxNewEvents {
			continue
		}
		added++
		events = append(events, store.GroupEvent{Text: text, Score: score, FirstAt: now, LastAt: now})
	}
	sort.SliceStable(events, func(i, j int) bool {
		if events[i].Score != events[j].Score {
			return events[i].Score > events[j].Score
		}
		return events[i].LastAt.After(events[j].LastAt)
	})
	if g.MaxItems > 0 && len(events) > g.MaxItems {
		events = events[:g.MaxItems]
	}
	if err := u.db.ReplaceGroupEvents(chatID, events, g.TTL); err != nil {
		return err
	}
	return done()
}

func sameText(events []store.GroupEvent, text string) (int, bool) {
	n := Normalize(text)
	for i, e := range events {
		if Normalize(e.Text) == n {
			return i, true
		}
	}
	return 0, false
}

// RenderGroup renders the top n events by score for a prompt, most recent
// first: "- (دیروز) …".
func RenderGroup(events []store.GroupEvent, n int, now time.Time) string {
	if n <= 0 || len(events) == 0 {
		return ""
	}
	top := append([]store.GroupEvent(nil), events...)
	sort.SliceStable(top, func(i, j int) bool { return top[i].Score > top[j].Score })
	if len(top) > n {
		top = top[:n]
	}
	sort.SliceStable(top, func(i, j int) bool { return top[i].LastAt.After(top[j].LastAt) })
	var lines []string
	for _, e := range top {
		days := int(now.Sub(e.LastAt).Hours() / 24)
		lines = append(lines, "- ("+i18n.Ago(days)+") "+e.Text)
	}
	return strings.Join(lines, "\n")
}
