package bot

import (
	"context"
	"log/slog"
	"time"

	"github.com/ehsanranjbar/kvutil/kv/memkv"

	"github.com/ehsanranjbar/hooshang/internal/auth"
	"github.com/ehsanranjbar/hooshang/internal/config"
	"github.com/ehsanranjbar/hooshang/internal/llm"
	"github.com/ehsanranjbar/hooshang/internal/ratelimit"
	"github.com/ehsanranjbar/hooshang/internal/store"
)

// newTestConfig returns a valid config for tests (owner 1, generous limits).
func newTestConfig() *config.Config {
	cfg := &config.Config{}
	cfg.Telegram.Token = "t"
	cfg.Telegram.OwnerID = 1
	cfg.LLM.BaseURL = "http://localhost:1234/v1"
	cfg.LLM.APIKey = "lm-studio"
	cfg.LLM.Model = "test-model"
	cfg.LLM.MemoryModel = "test-model"
	cfg.LLM.Temperature = 0.9
	cfg.LLM.MaxTokens = 2048
	cfg.LLM.Timeout = 120 * time.Second
	cfg.LLM.Tools = true
	cfg.Bot.Name = "هوشنگ"
	cfg.Bot.Aliases = []string{"هوشی", "hooshang"}
	cfg.Bot.Language = "fa"
	cfg.Bot.HistoryMessages = 30
	cfg.Bot.HistoryWindow = 20 * time.Minute
	cfg.Bot.ProfileBudget = 3000
	cfg.Bot.MessageRetention = 72 * time.Hour
	cfg.Bot.SpontaneousChance = 1
	cfg.Bot.SpontaneousCooldown = 5 * time.Minute
	cfg.Bot.MinMsgsBetween = 1
	cfg.Bot.Modes = map[string]int{"joke": 3, "tease": 3, "flirt": 1, "compliment": 2, "comment": 2}
	cfg.Bot.Human.Bubbles = 3
	cfg.Bot.RateLimit.GlobalPerMinute = 1000
	cfg.Bot.RateLimit.ChatPerMinute = 1000
	cfg.Features.Profiles = &config.ProfilesFeature{Enabled: true, UpdateEvery: 15, MaxAge: 24 * time.Hour, ContextMessages: 150, MaxItems: 10}
	cfg.LLM.ContextTokens = 32768
	cfg.Features.Faal = &config.FaalFeature{Enabled: true}
	cfg.Features.Poem = &config.PoemFeature{Enabled: true}
	cfg.Features.Roast = &config.RoastFeature{Enabled: true}
	cfg.Storage.Path = "./data"
	return cfg
}

// newTestBot builds a Bot wired to a memkv store and a fake TG. The bot's own
// ID is set to botID (bypassing GetMe, which needs a concrete *telego.Bot).
func newTestBot(tg TG, botID int64, botUsername string) (*Bot, *store.DB) {
	cfg := newTestConfig()
	db, err := store.OpenWith(memkv.New(), time.Hour)
	if err != nil {
		panic(err)
	}
	b := &Bot{
		deps: Deps{
			Cfg:     cfg,
			TG:      tg,
			DB:      db,
			LLM:     &llm.Fake{},
			Limiter: ratelimit.New(cfg.Bot.RateLimit.GlobalPerMinute, cfg.Bot.RateLimit.ChatPerMinute, time.Now),
			Admins:  &auth.Owner{ID: cfg.Telegram.OwnerID},
		},
		logger:      slog.New(slog.NewTextHandler(discardWriter{}, nil)),
		botID:       botID,
		botUsername: botUsername,
		accessCache: make(map[int64]accessEntry),
		sleepFn:     func(context.Context, time.Duration) {}, // no typing delays in tests
	}
	b.workers = newWorkers(b)
	return b, db
}

// discardWriter discards log output in tests.
type discardWriter struct{}

func (discardWriter) Write(p []byte) (int, error) { return len(p), nil }

// addItems stores items of a kind in a member's memory (test helper).
func addItems(db *store.DB, chatID, userID int64, kind string, locked bool, texts ...string) {
	now := time.Now()
	_, _ = db.UpdateMemory(chatID, userID, func(m *store.Memory) {
		for _, t := range texts {
			m.Add(store.Item{Kind: kind, Text: t, Locked: locked, Seen: 2, FirstAt: now, LastAt: now})
		}
	})
}

// addTerm stores a glossary term in the group's memory (test helper).
func addTerm(db *store.DB, chatID int64, term, meaning string, by int64) {
	now := time.Now()
	_, _ = db.UpdateMemory(chatID, store.GroupEntity, func(m *store.Memory) {
		m.Add(store.Item{Kind: store.KindTerm, Text: term, Note: meaning, By: by, Seen: 1, FirstAt: now, LastAt: now})
	})
}

// termOf returns the group's term item, or nil.
func termOf(db *store.DB, chatID int64, term string) *store.Item {
	m, _ := db.GetMemory(chatID, store.GroupEntity)
	for _, it := range m.Kind(store.KindTerm) {
		if it.Text == term {
			it := it
			return &it
		}
	}
	return nil
}

// kindTexts returns the texts of a kind in an entity's memory.
func kindTexts(db *store.DB, chatID, entity int64, kind string) []string {
	m, _ := db.GetMemory(chatID, entity)
	var out []string
	for _, it := range m.Kind(kind) {
		out = append(out, it.Text)
	}
	return out
}
