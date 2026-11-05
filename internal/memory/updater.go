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
	MaxItems        int
	Language        string
	MaxTokens       int // for the LLM call; 0 → 2048
	// JSONSchema sends response_format json_schema. Off by default: on Gemma 4
	// and Qwen 3.8 in LM Studio the constrained output degenerates (docs/MODELS.md);
	// the prompt spells out the JSON shape and parseExtracted validates it.
	JSONSchema bool
	Group      GroupConfig
}

// job is one queued profile update.
type job struct {
	chatID, userID int64
}

// Updater re-extracts member profiles from recent messages.
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
	return &Updater{
		db:      db,
		c:       c,
		model:   model,
		cfg:     cfg,
		logger:  logger,
		queue:   make(chan job, 64),
		pending: make(map[job]bool),
	}
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
			if j.userID == 0 {
				err = u.UpdateGroupNow(ctx, j.chatID)
			} else {
				err = u.UpdateNow(ctx, j.chatID, j.userID)
			}
			if err != nil {
				u.logger.Warn("memory: update failed", "chat", j.chatID, "user", j.userID, "err", err)
			}
		}
	}
}

// ProfileJSON is the shape of a profile with sections as plain string lists,
// without the locked flags. It is used in the extract prompt and is the
// expected shape of the model's JSON output.
type ProfileJSON struct {
	Nickname      string   `json:"nickname"`
	Summary       string   `json:"summary"`
	Facts         []string `json:"facts"`
	Interests     []string `json:"interests"`
	Quirks        []string `json:"quirks"`
	Relationships []string `json:"relationships"`
	Boundaries    []string `json:"boundaries"`
}

// ToProfileJSON strips the locked flags from a stored profile.
func ToProfileJSON(p store.Profile) ProfileJSON {
	ls := func(items []store.Item) []string {
		out := make([]string, 0, len(items))
		for _, it := range items {
			if t := strings.TrimSpace(it.Text); t != "" {
				out = append(out, t)
			}
		}
		return out
	}
	return ProfileJSON{
		Nickname:      p.Nickname,
		Summary:       p.Summary,
		Facts:         ls(p.Facts),
		Interests:     ls(p.Interests),
		Quirks:        ls(p.Quirks),
		Relationships: ls(p.Relationships),
		Boundaries:    ls(p.Boundaries),
	}
}

// String renders the profile JSON compactly for prompts.
func (p ProfileJSON) String() string {
	s, err := json.Marshal(p)
	if err != nil {
		return "{}"
	}
	return string(s)
}

// FromProfileJSON fills a stored profile from the extracted JSON.
func FromProfileJSON(pj ProfileJSON) store.Profile {
	items := func(ss []string) []store.Item {
		out := make([]store.Item, 0, len(ss))
		for _, s := range ss {
			if t := strings.TrimSpace(s); t != "" {
				out = append(out, store.Item{Text: t})
			}
		}
		return out
	}
	return store.Profile{
		Nickname:      strings.TrimSpace(pj.Nickname),
		Summary:       strings.TrimSpace(pj.Summary),
		Facts:         items(pj.Facts),
		Interests:     items(pj.Interests),
		Quirks:        items(pj.Quirks),
		Relationships: items(pj.Relationships),
		Boundaries:    items(pj.Boundaries),
	}
}

// profileSchema is the response_format schema for the extract call.
// NOTE: the non-strict schema is only a hint; the prompt also spells out the
// exact shape with an example, and parseExtracted validates the result.
var profileSchema = json.RawMessage(`{
  "type": "object",
  "properties": {
    "nickname": {"type": "string"},
    "summary": {"type": "string"},
    "facts": {"type": "array", "items": {"type": "string"}},
    "interests": {"type": "array", "items": {"type": "string"}},
    "quirks": {"type": "array", "items": {"type": "string"}},
    "relationships": {"type": "array", "items": {"type": "string"}},
    "boundaries": {"type": "array", "items": {"type": "string"}}
  }
}`)

// parseExtracted validates the model's JSON: unknown keys are ignored,
// missing sections are treated as empty. It fails if the top level is not an
// object or none of the known keys is present.
func parseExtracted(raw []byte) (ProfileJSON, error) {
	var probe map[string]json.RawMessage
	if err := json.Unmarshal(raw, &probe); err != nil {
		return ProfileJSON{}, fmt.Errorf("not a JSON object: %w", err)
	}
	known := []string{"nickname", "summary", "facts", "interests", "quirks", "relationships", "boundaries"}
	any := false
	for _, k := range known {
		if _, ok := probe[k]; ok {
			any = true
			break
		}
	}
	if !any {
		return ProfileJSON{}, fmt.Errorf("no known profile key in %q", strings.TrimSpace(string(raw)))
	}
	var pj ProfileJSON
	if err := json.Unmarshal(raw, &pj); err != nil {
		return ProfileJSON{}, err
	}
	return pj, nil
}

// UpdateNow runs one profile update for a member. It reads the member and
// recent messages, calls the LLM, and stores the merged profile. On a parse
// failure the old profile is kept and the counter is reset; it returns nil.
func (u *Updater) UpdateNow(ctx context.Context, chatID, userID int64) error {
	m, err := u.db.GetMember(chatID, userID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil
		}
		return err
	}
	msgs, err := u.db.RecentMessages(chatID, u.cfg.ContextMessages)
	if err != nil {
		return err
	}
	n := 0
	for _, msg := range msgs {
		if msg.UserID == userID && !msg.FromBot {
			n++
		}
	}
	if n < 3 {
		// Not enough material yet; just reset the counter.
		_, err := u.db.UpdateMember(chatID, userID, func(m *store.Member) { m.MsgsSinceUpdate = 0 })
		return err
	}

	var lines []string
	for _, msg := range msgs {
		if msg.FromBot {
			continue
		}
		marker := "  "
		if msg.UserID == userID {
			marker = ">>"
		}
		lines = append(lines, fmt.Sprintf("%s %s: %s", marker, msg.Name, msg.Text))
	}
	prompt, err := prompts.MemoryExtract(prompts.ExtractData{
		Language:   u.cfg.Language,
		TargetName: m.FirstName,
		Profile:    ToProfileJSON(m.Profile).String(),
		Transcript: strings.Join(lines, "\n"),
	})
	if err != nil {
		return err
	}

	maxTokens := u.cfg.MaxTokens
	if maxTokens < 2048 {
		maxTokens = 2048
	}
	req := llm.Request{
		Model:       u.model,
		Messages:    []llm.Message{{Role: "user", Content: prompt}},
		Temperature: 0.2,
		MaxTokens:   maxTokens,
	}
	if u.cfg.JSONSchema {
		req.Schema, req.SchemaName = profileSchema, "profile"
	}
	resp, err := u.c.Chat(ctx, req)
	if err != nil {
		u.logger.Warn("memory: llm call failed, resetting counter", "chat", chatID, "user", userID, "err", err)
		_, err := u.db.UpdateMember(chatID, userID, func(m *store.Member) { m.MsgsSinceUpdate = 0 })
		return err
	}

	raw, err := llm.ExtractJSON(resp.Content)
	if err != nil {
		u.logger.Warn("memory: extract parse failed, keeping old profile", "chat", chatID, "user", userID, "err", err)
		_, err := u.db.UpdateMember(chatID, userID, func(m *store.Member) { m.MsgsSinceUpdate = 0 })
		return err
	}
	pj, err := parseExtracted(raw)
	if err != nil {
		u.logger.Warn("memory: extract invalid, keeping old profile", "chat", chatID, "user", userID, "err", err)
		_, err := u.db.UpdateMember(chatID, userID, func(m *store.Member) { m.MsgsSinceUpdate = 0 })
		return err
	}
	extracted := FromProfileJSON(pj)

	// Merge inside the transaction against the *current* stored profile so
	// concurrent Locked items and Suppressed are kept (KVUTIL.md rule).
	_, err = u.db.UpdateMember(chatID, userID, func(m *store.Member) {
		m.Profile = Merge(m.Profile, extracted, u.cfg.MaxItems)
		m.ProfileUpdatedAt = time.Now()
		m.MsgsSinceUpdate = 0
	})
	return err
}
