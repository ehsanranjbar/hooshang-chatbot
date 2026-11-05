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
	"github.com/ehsanranjbar/hooshang/internal/transcribe"
)

func main() {
	configPath := flag.String("config", "config.yaml", "path to the YAML config file")
	checkOnly := flag.Bool("check", false, "load and validate the config, print the features that are on and the tools they need, and exit")
	flag.Parse()

	if *checkOnly {
		os.Exit(runCheck(*configPath, os.Stdout))
	}

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))

	cfg, err := config.Load(*configPath)
	if err != nil {
		logger.Error("config", "err", err)
		os.Exit(1)
	}
	logger.Info("features", "on", cfg.Features.Active())

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
	updater := newUpdater(cfg, db, client, logger)
	faalImage, poemImage := newRenderers(cfg, logger)

	deps := bot.Deps{
		Cfg:         cfg,
		TG:          tg,
		DB:          db,
		LLM:         client,
		Limiter:     ratelimit.New(cfg.Bot.RateLimit.GlobalPerMinute, cfg.Bot.RateLimit.ChatPerMinute, time.Now),
		Admins:      auth.Owner{ID: cfg.Telegram.OwnerID},
		Memory:      updater,
		Transcriber: newTranscriber(cfg, logger),
		FaalImage:   faalImage,
		PoemImage:   poemImage,
	}
	if cfg.Features.Faal.On() || cfg.Features.Poem.On() {
		deps.Ganjoor = ganjoor.New("", &http.Client{Timeout: 30 * time.Second})
	}
	b, err := bot.New(deps, logger)
	if err != nil {
		logger.Error("create bot", "err", err)
		os.Exit(1)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	updaterDone := make(chan struct{})
	go func() {
		if updater != nil {
			updater.Run(ctx)
		}
		close(updaterDone)
	}()

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

// newUpdater returns the memory updater, or nil when neither profiles nor
// group memory is on (the bot then queues no memory work).
func newUpdater(cfg *config.Config, db *store.DB, client llm.Client, logger *slog.Logger) *memory.Updater {
	f := cfg.Features
	if !f.Profiles.On() && !f.GroupMemory.On() {
		return nil
	}
	uc := memory.UpdaterConfig{
		ContextMessages: 150, // profile settings; unused while profiles are off
		MaxItems:        10,
		Language:        cfg.Bot.Language,
		MaxTokens:       cfg.LLM.MaxTokens,
		JSONSchema:      cfg.LLM.JSONSchema,
		Group:           groupConfig(cfg),
	}
	if p := f.Profiles; p.On() {
		uc.ContextMessages, uc.MaxItems = p.ContextMessages, p.MaxItems
	}
	return memory.NewUpdater(db, client, cfg.LLM.MemoryModel, uc, logger)
}

// newRenderers builds the Nastaliq card renderer for the faal and the poem
// when either wants an image; a nil renderer sends text. A font or shaper
// problem is a warning, not a failure.
func newRenderers(cfg *config.Config, logger *slog.Logger) (faal, poem *faalimg.Renderer) {
	faalWants := cfg.Features.Faal.On() && cfg.Features.Faal.Image
	poemWants := cfg.Features.Poem.On() && cfg.Features.Poem.Image
	if !faalWants && !poemWants {
		return nil, nil
	}
	r, err := faalimg.New(cfg.Nastaliq.Font, faalimg.Shaper(cfg.Nastaliq.Shaper))
	if err != nil {
		logger.Warn("nastaliq images disabled", "err", err)
		return nil, nil
	}
	logger.Info("nastaliq images", "font", cfg.Nastaliq.Font, "shaper", r.Engine(), "faal", faalWants, "poems", poemWants)
	if faalWants {
		faal = r
	}
	if poemWants {
		poem = r
	}
	return faal, poem
}

// newTranscriber returns the voice transcriber when features.voice is on and
// whisper-cli, ffmpeg and the model are all there; otherwise nil (with a
// warning): voice messages are then stored as «[ویس]», as without the feature.
func newTranscriber(cfg *config.Config, logger *slog.Logger) transcribe.Transcriber {
	v := cfg.Features.Voice
	if !v.On() {
		return nil
	}
	w := whisperFor(cfg)
	if err := w.Check(); err != nil {
		logger.Warn("voice transcription disabled", "err", err)
		return nil
	}
	whisper, ffmpeg, model := w.Paths()
	logger.Info("voice transcription", "whisper", whisper, "ffmpeg", ffmpeg, "model", model, "language", v.Language)
	return w
}

// whisperFor builds the whisper transcriber from the voice settings; ffmpeg is
// features.media.ffmpeg, else the one in PATH.
func whisperFor(cfg *config.Config) *transcribe.Whisper {
	v := cfg.Features.Voice
	return transcribe.NewWhisper(transcribe.Options{
		Whisper: v.Whisper, FFmpeg: cfg.FFmpeg(), Model: v.Model, Language: v.Language, Threads: v.Threads,
	})
}

// groupConfig is the updater's group memory config; a zero TTL disables it.
func groupConfig(cfg *config.Config) memory.GroupConfig {
	g := cfg.Features.GroupMemory
	if !g.On() {
		return memory.GroupConfig{}
	}
	return memory.GroupConfig{TTL: g.TTL, MaxItems: g.MaxItems}
}
