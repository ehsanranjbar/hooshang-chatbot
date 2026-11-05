// Command hooshang runs the هوشنگ Telegram bot.
package main

import (
	"context"
	"flag"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/mymmrac/telego"

	"github.com/ehsanranjbar/hooshang/internal/auth"
	"github.com/ehsanranjbar/hooshang/internal/bot"
	"github.com/ehsanranjbar/hooshang/internal/config"
	"github.com/ehsanranjbar/hooshang/internal/faalimg"
	"github.com/ehsanranjbar/hooshang/internal/ganjoor"
	"github.com/ehsanranjbar/hooshang/internal/llm"
	"github.com/ehsanranjbar/hooshang/internal/memory"
	"github.com/ehsanranjbar/hooshang/internal/ratelimit"
	"github.com/ehsanranjbar/hooshang/internal/store"
)

func main() {
	configPath := flag.String("config", "config.yaml", "path to the YAML config file")
	flag.Parse()

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))

	cfg, err := config.Load(*configPath)
	if err != nil {
		logger.Error("config", "err", err)
		os.Exit(1)
	}

	db, err := store.Open(cfg.Storage.Path, cfg.Bot.MessageRetention)
	if err != nil {
		logger.Error("open store", "err", err)
		os.Exit(1)
	}
	defer db.Close()

	tg, err := telego.NewBot(cfg.Telegram.Token)
	if err != nil {
		logger.Error("create telegram bot", "err", err)
		os.Exit(1)
	}

	client := llm.New(cfg.LLM.BaseURL, cfg.LLM.APIKey, cfg.LLM.Timeout)
	updater := memory.NewUpdater(db, client,
		cfg.LLM.MemoryModel, memory.UpdaterConfig{
			ContextMessages: cfg.Memory.ContextMessages,
			MaxItems:        cfg.Memory.MaxItems,
			Language:        cfg.Bot.Language,
			MaxTokens:       cfg.LLM.MaxTokens,
			JSONSchema:      cfg.LLM.JSONSchema,
			Group:           groupConfig(cfg),
		}, logger)

	var faalImage, poemImage *faalimg.Renderer
	if cfg.Faal.Image || cfg.Faal.PoemImages {
		if faalImage, err = faalimg.New(cfg.Faal.Font, faalimg.Shaper(cfg.Faal.Shaper)); err != nil {
			logger.Warn("faal image disabled", "err", err)
		} else {
			logger.Info("poem images", "font", cfg.Faal.Font, "shaper", faalImage.Engine(),
				"faal", cfg.Faal.Image, "poems", cfg.Faal.PoemImages)
			if cfg.Faal.PoemImages {
				poemImage = faalImage
			}
			if !cfg.Faal.Image {
				faalImage = nil
			}
		}
	}

	b, err := bot.New(bot.Deps{
		Cfg:       cfg,
		TG:        tg,
		DB:        db,
		LLM:       client,
		Ganjoor:   ganjoor.New("", &http.Client{Timeout: 30 * time.Second}),
		Limiter:   ratelimit.New(cfg.Bot.RateLimit.GlobalPerMinute, cfg.Bot.RateLimit.ChatPerMinute, time.Now),
		Admins:    auth.Owner{ID: cfg.Telegram.OwnerID},
		Memory:    updater,
		FaalImage: faalImage,
		PoemImage: poemImage,
	}, logger)
	if err != nil {
		logger.Error("create bot", "err", err)
		os.Exit(1)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	updaterDone := make(chan struct{})
	go func() { updater.Run(ctx); close(updaterDone) }()

	logger.Info("hooshang started", "name", cfg.Bot.Name, "model", cfg.LLM.Model)
	runErr := b.Run(ctx) // returns after its workers have stopped
	stop()
	select { // the updater may be mid-write; let it finish before the DB closes
	case <-updaterDone:
	case <-time.After(10 * time.Second):
		logger.Warn("memory updater did not stop in time")
	}
	if runErr != nil {
		logger.Error("bot run", "err", runErr)
		db.Close()
		os.Exit(1)
	}
	logger.Info("hooshang stopped")
}

// groupConfig is the updater's group memory config; a zero TTL disables it.
func groupConfig(cfg *config.Config) memory.GroupConfig {
	if !cfg.GroupMemory.Enabled {
		return memory.GroupConfig{}
	}
	return memory.GroupConfig{TTL: cfg.GroupMemory.TTL, MaxItems: cfg.GroupMemory.MaxItems}
}
