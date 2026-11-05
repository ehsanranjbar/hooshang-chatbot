package bot

import (
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
	cfg.Bot.RateLimit.GlobalPerMinute = 1000
	cfg.Bot.RateLimit.ChatPerMinute = 1000
	cfg.Memory.UpdateEvery = 15
	cfg.Memory.MaxAge = 24 * time.Hour
	cfg.Memory.ContextMessages = 150
	cfg.Memory.MaxItems = 10
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
	}
	b.workers = newWorkers(b)
	return b, db
}

// discardWriter discards log output in tests.
type discardWriter struct{}

func (discardWriter) Write(p []byte) (int, error) { return len(p), nil }
