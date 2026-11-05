package memory

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/ehsanranjbar/hooshang/internal/llm"
	"github.com/ehsanranjbar/hooshang/internal/prompts"
	"github.com/ehsanranjbar/hooshang/internal/store"
)

// UpdaterConfig holds the updater's settings.
type UpdaterConfig struct {
	ContextMessages int
	MaxItems        int // per kind
	Language        string
	MaxTokens       int // for the LLM call; 0 → 2048
	JSONSchema      bool
	BotID           int64 // the bot's user id: the self entity
	Group           GroupConfig
	Analysis        AnalysisConfig
	// Embed, when set, is told about new or changed items so they can be
	// embedded for retrieval (RAG). It must not block.
	Embed func(chatID, entity int64, items []store.Item)
}

// job is one queued update: a member (userID), the group (0) or the bot (BotID).
type job struct {
	chatID, userID int64
}

// Updater re-extracts memories from recent messages.
type Updater struct {
	db     *store.DB
	c      llm.Client
	model  string
	cfg    UpdaterConfig
	logger *slog.Logger

	mu      sync.Mutex
	queue   chan job
	pending map[job]bool
}

// NewUpdater returns an Updater with a 64-slot queue.
func NewUpdater(db *store.DB, c llm.Client, model string, cfg UpdaterConfig, logger *slog.Logger) *Updater {
	if logger == nil {
		logger = slog.Default()
	}
	return &Updater{db: db, c: c, model: model, cfg: cfg, logger: logger, queue: make(chan job, 64), pending: make(map[job]bool)}
}

// QueueLen reports how many jobs are waiting.
func (u *Updater) QueueLen() int { return len(u.queue) }

// Enqueue queues (chatID, userID) for an update. It is non-blocking: jobs
// already pending are deduplicated, and a full queue drops the job.
func (u *Updater) Enqueue(chatID, userID int64) {
	j := job{chatID, userID}
	u.mu.Lock()
	if u.pending[j] {
		u.mu.Unlock()
		return
	}
	select {
	case u.queue <- j:
		u.pending[j] = true
	default:
		u.logger.Warn("memory: update queue full, dropping", "chat", chatID, "user", userID)
	}
	u.mu.Unlock()
}

// EnqueueSelf queues an update of the bot's own memory in the chat.
func (u *Updater) EnqueueSelf(chatID int64) {
	if u.cfg.BotID != 0 {
		u.Enqueue(chatID, u.cfg.BotID)
	}
}

// Run runs the single worker until ctx is done.
func (u *Updater) Run(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case j := <-u.queue:
			u.mu.Lock()
			delete(u.pending, j)
			u.mu.Unlock()
			var err error
			switch {
			case j.userID == store.GroupEntity:
				err = u.UpdateGroupNow(ctx, j.chatID)
			case j.userID == u.cfg.BotID:
				err = u.UpdateSelfNow(ctx, j.chatID)
			default:
				err = u.UpdateNow(ctx, j.chatID, j.userID)
			}
			if err != nil {
				u.logger.Warn("memory: update failed", "chat", j.chatID, "user", j.userID, "err", err)
			}
		}
	}
}

// extractedJSON is the shape the extractors return.
type extractedJSON struct {
	Nickname string `json:"nickname"`
	Summary  string `json:"summary"`
	Items    *[]struct {
		Kind string  `json:"kind"`
		Text string  `json:"text"`
		Msgs []int64 `json:"msgs"`
	} `json:"items"`
}

// extractSchema is the response_format schema for the extract calls (a hint:
// the prompt spells the shape out and parseExtracted validates).
var extractSchema = json.RawMessage(`{
  "type": "object",
  "properties": {
    "nickname": {"type": "string"},
    "summary": {"type": "string"},
    "items": {"type": "array", "items": {"type": "object", "properties": {
      "kind": {"type": "string"}, "text": {"type": "string"}, "msgs": {"type": "array", "items": {"type": "integer"}}},
      "required": ["kind", "text"]}}
  },
  "required": ["items"]
}`)

// parseExtracted validates the model's JSON: it must be an object with an
// "items" array. Unknown keys are ignored.
func parseExtracted(raw []byte) (Extracted, error) {
	var x extractedJSON
	if err := json.Unmarshal(raw, &x); err != nil {
		return Extracted{}, fmt.Errorf("not the expected JSON: %w", err)
	}
	if x.Items == nil {
		return Extracted{}, fmt.Errorf(`no "items" key in %q`, strings.TrimSpace(string(raw)))
	}
	out := Extracted{Nickname: strings.TrimSpace(x.Nickname), Summary: strings.TrimSpace(x.Summary)}
	for _, it := range *x.Items {
		out.Items = append(out.Items, ExtractedItem{Kind: strings.ToLower(strings.TrimSpace(it.Kind)), Text: strings.TrimSpace(it.Text), Msgs: it.Msgs})
	}
	return out, nil
}

// CurrentItems renders a memory's items for an extract prompt, one "kind: text"
// per line, so the model knows what is already known.
func CurrentItems(m store.Memory, kinds []string) string {
	var lines []string
	for _, kind := range kinds {
		for _, it := range m.Kind(kind) {
			lines = append(lines, kind+": "+it.Text)
		}
	}
	return strings.Join(lines, "\n")
}

// extract runs one extraction call and parses it.
func (u *Updater) extract(ctx context.Context, prompt string) (Extracted, error) {
	maxTokens := max(u.cfg.MaxTokens, 2048)
	req := llm.Request{Model: u.model, Messages: []llm.Message{{Role: "user", Content: prompt}}, Temperature: 0.2, MaxTokens: maxTokens}
	if u.cfg.JSONSchema {
		req.Schema, req.SchemaName = extractSchema, "memory"
	}
	resp, err := u.c.Chat(ctx, req)
	if err != nil {
		return Extracted{}, fmt.Errorf("llm: %w", err)
	}
	raw, err := llm.ExtractJSON(resp.Content)
	if err != nil {
		return Extracted{}, fmt.Errorf("parse: %w", err)
	}
	return parseExtracted(raw)
}

// UpdateNow runs one memory update for a member: it reads recent messages,
// calls the model, and merges the result into the member's memory. A failed
// call or an invalid answer keeps the old memory; it returns nil for those.
func (u *Updater) UpdateNow(ctx context.Context, chatID, userID int64) error {
	m, err := u.db.GetMember(chatID, userID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil
		}
		return err
	}
	reset := func() error {
		_, err := u.db.UpdateMember(chatID, userID, func(m *store.Member) { m.MsgsSinceUpdate = 0 })
		return err
	}
	msgs, err := u.db.RecentMessages(chatID, u.cfg.ContextMessages)
	if err != nil {
		return err
	}
	n := 0
	var lines []string
	for _, msg := range msgs {
		if msg.FromBot {
			continue
		}
		marker := "  "
		if msg.UserID == userID {
			marker = ">>"
			n++
		}
		lines = append(lines, fmt.Sprintf("%s [%d] %s: %s", marker, msg.ID, msg.Name, msg.Text))
	}
	if n < 3 {
		return reset() // not enough material yet
	}
	cur, err := u.db.GetMemory(chatID, userID)
	if err != nil {
		return err
	}
	prompt, err := prompts.MemoryExtract(prompts.ExtractData{
		Language: u.cfg.Language, TargetName: m.FirstName, Nickname: cur.Nickname, Summary: cur.Summary,
		Items: CurrentItems(cur, MemberKinds), Kinds: strings.Join(MemberKinds, ", "), Transcript: strings.Join(lines, "\n"),
	})
	if err != nil {
		return err
	}
	ext, err := u.extract(ctx, prompt)
	if err != nil {
		u.logger.Warn("memory: extraction failed, keeping the old memory", "chat", chatID, "user", userID, "err", err)
		return reset()
	}
	// Merge inside the transaction against the *current* stored memory so
	// concurrent admin edits (locked items, suppressed texts) are kept.
	mem, err := u.db.UpdateMemory(chatID, userID, func(mm *store.Memory) { Merge(mm, ext, MemberKinds, time.Now(), u.cfg.MaxItems) })
	if err != nil {
		return err
	}
	u.embed(chatID, userID, mem)
	m, err = u.db.UpdateMember(chatID, userID, func(m *store.Member) {
		m.ProfileUpdatedAt = time.Now()
		m.MsgsSinceUpdate = 0
	})
	if err != nil {
		return err
	}
	if u.analysisDue(m, mem) {
		if err := u.AnalyseNow(ctx, chatID, userID); err != nil {
			u.logger.Warn("memory: analysis failed, keeping the old one", "chat", chatID, "user", userID, "err", err)
		}
	}
	return nil
}

// UpdateSelfNow updates what the bot remembers about itself in the chat: the
// stances it took, the running jokes it made and the promises it gave, from
// its own recent messages and the messages they answered.
func (u *Updater) UpdateSelfNow(ctx context.Context, chatID int64) error {
	done := func() error {
		return u.db.UpdateChat(chatID, func(c *store.Chat) { c.MsgsSinceSelfUpdate = 0 })
	}
	msgs, err := u.db.RecentMessages(chatID, u.cfg.ContextMessages)
	if err != nil {
		return err
	}
	byID := map[int64]store.Message{}
	for _, m := range msgs {
		byID[m.ID] = m
	}
	var lines []string
	n := 0
	for _, m := range msgs {
		if !m.FromBot || strings.HasPrefix(m.Text, "[") {
			continue
		}
		n++
		if r, ok := byID[m.ReplyToID]; ok && m.ReplyToID != 0 {
			lines = append(lines, fmt.Sprintf("   [%d] %s: %s", r.ID, r.Name, r.Text))
		}
		lines = append(lines, fmt.Sprintf(">> [%d] you: %s", m.ID, m.Text))
	}
	if n < 5 {
		return done()
	}
	cur, err := u.db.GetMemory(chatID, u.cfg.BotID)
	if err != nil {
		return err
	}
	prompt, err := prompts.SelfExtract(prompts.ExtractData{
		Language: u.cfg.Language, Items: CurrentItems(cur, SelfKinds), Kinds: strings.Join(SelfKinds, ", "),
		Transcript: strings.Join(lines, "\n"),
	})
	if err != nil {
		return err
	}
	ext, err := u.extract(ctx, prompt)
	if err != nil {
		u.logger.Warn("self memory: extraction failed", "chat", chatID, "err", err)
		return done()
	}
	mem, err := u.db.UpdateMemory(chatID, u.cfg.BotID, func(mm *store.Memory) { Merge(mm, ext, SelfKinds, time.Now(), u.cfg.MaxItems) })
	if err != nil {
		return err
	}
	u.embed(chatID, u.cfg.BotID, mem)
	return done()
}

// embed hands the memory's items to the embedder, if any.
func (u *Updater) embed(chatID, entity int64, m store.Memory) {
	if u.cfg.Embed != nil {
		u.cfg.Embed(chatID, entity, m.Items)
	}
}
