package main

import (
	"fmt"
	"io"
	"os/exec"
	"strings"

	"github.com/ehsanranjbar/hooshang/internal/config"
	"github.com/ehsanranjbar/hooshang/internal/faalimg"
)

// runCheck implements -check: load and validate the config, then report the
// features that are on and whether the tools they need were found. It prints
// no secrets (not the token, not the API key) and returns the exit code: 1 for
// an invalid config or a feature that is on but cannot work, else 0.
func runCheck(path string, w io.Writer) int {
	cfg, err := config.Load(path)
	if err != nil {
		fmt.Fprintf(w, "config: %v\n", err)
		return 1
	}
	fmt.Fprintf(w, "config %s: ok\n", path)
	fmt.Fprintf(w, "bot: %s, model %s at %s, owner %d\n", cfg.Bot.Name, cfg.LLM.Model, cfg.LLM.BaseURL, cfg.Telegram.OwnerID)
	on := cfg.Features.Active()
	fmt.Fprintf(w, "features on (%d): %s\n", len(on), featureNames(on))
	fmt.Fprintf(w, "storage: %s\n", cfg.Storage.Path)

	failed := false
	if cfg.Features.Media.On() {
		if p, err := exec.LookPath(orDefault(cfg.FFmpeg(), "ffmpeg")); err == nil {
			fmt.Fprintf(w, "media: ffmpeg %s (animated stickers and GIFs are described from %d frames)\n", p, cfg.Features.Media.Frames)
		} else {
			fmt.Fprintln(w, "media: ffmpeg not found (animated stickers and GIFs use their thumbnail)")
		}
	}
	if f, p := cfg.Features.Faal, cfg.Features.Poem; (f.On() && f.Image) || (p.On() && p.Image) {
		if r, err := faalimg.New(cfg.Nastaliq.Font, faalimg.Shaper(cfg.Nastaliq.Shaper)); err == nil {
			fmt.Fprintf(w, "nastaliq: shaper %s\n", r.Engine())
		} else {
			fmt.Fprintf(w, "nastaliq: %v (cards are sent as text)\n", err)
		}
	}
	if v := cfg.Features.Voice; v.On() {
		wh := whisperFor(cfg)
		whisper, ffmpeg, model := wh.Paths()
		fmt.Fprintf(w, "voice: whisper-cli %s, ffmpeg %s, model %s, language %s\n", orDefault(whisper, "NOT FOUND"), orDefault(ffmpeg, "NOT FOUND"), model, v.Language)
		if err := wh.Check(); err != nil {
			fmt.Fprintf(w, "voice: %v\n", err)
			failed = true
		}
	}
	if failed {
		return 1
	}
	return 0
}

// featureNames renders the list of feature names, or "none".
func featureNames(on []string) string {
	if len(on) == 0 {
		return "none"
	}
	return strings.Join(on, ", ")
}

// orDefault returns s, or def when s is empty.
func orDefault(s, def string) string {
	if s == "" {
		return def
	}
	return s
}
