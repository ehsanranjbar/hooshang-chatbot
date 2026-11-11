// Package config loads hooshang's YAML configuration, applies environment
// overrides, and validates the result.
package config

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Config is the full hooshang configuration.
type Config struct {
	Telegram struct {
		Token    string  `yaml:"token"`     // env HOOSHANG_TELEGRAM_TOKEN overrides
		AdminIDs []int64 `yaml:"admin_ids"` // required, at least one, each > 0
	} `yaml:"telegram"`
	LLM struct {
		BaseURL     string        `yaml:"base_url"` // default http://localhost:1234/v1
		APIKey      string        `yaml:"api_key"`  // env HOOSHANG_LLM_API_KEY overrides; default "lm-studio"
		Model       string        `yaml:"model"`    // required
		MemoryModel string        `yaml:"memory_model"`
		Temperature float64       `yaml:"temperature"` // [0, 2], default 0.9 (memory calls always use 0.2)
		MaxTokens   int           `yaml:"max_tokens"`  // default 2048, min 1024
		Timeout     time.Duration `yaml:"timeout"`     // default 120s
		Tools       bool          `yaml:"tools"`       // default true
		JSONSchema  bool          `yaml:"json_schema"` // send response_format json_schema for memory extraction; default false
		// EmbeddingModel is the embedding model served next to the chat model
		// (e.g. text-embedding-bge-m3); "" turns retrieval (features.rag) off.
		EmbeddingModel string `yaml:"embedding_model"`
		// EmbeddingDims is the number of dimensions of the embedding model's
		// vectors (1024 for bge-m3). A change wipes the stored vectors.
		EmbeddingDims int `yaml:"embedding_dims"`
		// ContextTokens is the context window the chat model is loaded with;
		// prompts are trimmed to fit under it minus max_tokens. Default 32768.
		ContextTokens int `yaml:"context_tokens"`
	} `yaml:"llm"`
	Bot struct {
		Name                string         `yaml:"name"` // default "هوشنگ"
		Aliases             []string       `yaml:"aliases"`
		Language            string         `yaml:"language"` // default "fa"
		HistoryMessages     int            `yaml:"history_messages"`
		HistoryWindow       time.Duration  `yaml:"history_window"`  // only messages this recent are in a prompt
		ProfileBudget       int            `yaml:"profile_budget"`  // characters of member profiles per prompt; default 3000
		RandomProfiles      int            `yaml:"random_profiles"` // random other members added while the budget allows; default 2
		MessageRetention    time.Duration  `yaml:"message_retention"`
		SpontaneousChance   float64        `yaml:"spontaneous_chance"`
		ReactionChance      float64        `yaml:"reaction_chance"`   // chance of reacting with an emoji when not addressed, [0,1]; default 0.15
		ReactionCooldown    time.Duration  `yaml:"reaction_cooldown"` // min time between such reactions; default 1m
		SpontaneousCooldown time.Duration  `yaml:"spontaneous_cooldown"`
		MinMsgsBetween      int            `yaml:"min_msgs_between"`
		Modes               map[string]int `yaml:"modes"` // merges with defaults; 0 disables a mode
		RateLimit           struct {
			GlobalPerMinute int `yaml:"global_per_minute"`
			ChatPerMinute   int `yaml:"chat_per_minute"`
		} `yaml:"rate_limit"`
		// Persona is who the bot is, in the admin's words (English or Persian).
		Persona struct {
			Text  string   `yaml:"text"`  // replaces the built-in identity paragraph when set
			Notes []string `yaml:"notes"` // extra behaviour notes appended to the system prompt
		} `yaml:"persona"`
		// Human makes the bot's timing feel like a person's.
		Human struct {
			Typing struct {
				Enabled        bool          `yaml:"enabled"`          // wait as if typing before the first bubble; default true
				CharsPerSecond int           `yaml:"chars_per_second"` // default 25
				Min            time.Duration `yaml:"min"`              // default 1s
				Max            time.Duration `yaml:"max"`              // default 6s
			} `yaml:"typing"`
			LateChance float64       `yaml:"late_chance"` // chance of answering late, [0,1]; default 0.05
			LateMax    time.Duration `yaml:"late_max"`    // the longest "late"; default 45s
			QuietHours string        `yaml:"quiet_hours"` // "HH:MM-HH:MM" Tehran: no spontaneous messages, reactions or greetings; default "01:30-08:00"; "" = none
			Moods      []string      `yaml:"moods"`       // the moods a day is drawn from; empty = the built-in list (see prompts.Moods)
			Bubbles    int           `yaml:"bubbles"`     // most chat bubbles per reply; default 3; 1 = never split
		} `yaml:"human"`
	} `yaml:"bot"`
	// Features are the optional capabilities. A feature is on when its section
	// is present (and not "enabled: false"); see Features.
	Features Features `yaml:"features"`
	// Nastaliq are the rendering settings shared by the faal and poem cards.
	Nastaliq struct {
		Font   string `yaml:"font"`   // a Nastaliq .ttf; empty = embedded Gulzar (HarfBuzz) or Noto Nastaliq Urdu (go-text)
		Shaper string `yaml:"shaper"` // auto (HarfBuzz's hb-shape if installed, else go-text), harfbuzz, go
	} `yaml:"nastaliq"`
	Storage struct {
		Path string `yaml:"path"` // default ./data
	} `yaml:"storage"`
}

// defaults returns a Config with every field set to its default value.
// Load unmarshals the user's YAML on top of it: yaml.v3 leaves keys that are
// absent from the file untouched, so an explicit 0 in the file stays 0.
func defaults() *Config {
	c := &Config{}
	c.LLM.BaseURL = "http://localhost:1234/v1"
	c.LLM.APIKey = "lm-studio"
	c.LLM.Temperature = 0.9
	c.LLM.MaxTokens = 2048
	c.LLM.Timeout = 120 * time.Second
	c.LLM.Tools = true
	c.LLM.ContextTokens = 32768
	c.LLM.EmbeddingDims = 1024
	c.Bot.Name = "هوشنگ"
	c.Bot.Aliases = []string{"هوشی", "hooshang"}
	c.Bot.Language = "fa"
	c.Bot.HistoryMessages = 15
	c.Bot.HistoryWindow = 20 * time.Minute
	c.Bot.ProfileBudget = 3000
	c.Bot.RandomProfiles = 2
	c.Bot.MessageRetention = 72 * time.Hour
	c.Bot.SpontaneousChance = 0.07
	c.Bot.SpontaneousCooldown = 5 * time.Minute
	c.Bot.MinMsgsBetween = 8
	c.Bot.Modes = map[string]int{"joke": 3, "tease": 3, "flirt": 1, "compliment": 2, "comment": 2, "sticker": 1}
	c.Bot.Human.Typing.Enabled = true
	c.Bot.Human.Typing.CharsPerSecond = 25
	c.Bot.Human.Typing.Min = time.Second
	c.Bot.Human.Typing.Max = 6 * time.Second
	c.Bot.Human.LateChance = 0.05
	c.Bot.Human.LateMax = 45 * time.Second
	c.Bot.Human.QuietHours = "01:30-08:00"
	c.Bot.Human.Bubbles = 3
	c.Bot.RateLimit.GlobalPerMinute = 10
	c.Bot.RateLimit.ChatPerMinute = 6
	c.Bot.ReactionChance = 0.15
	c.Bot.ReactionCooldown = time.Minute
	c.Nastaliq.Shaper = "auto"
	c.Storage.Path = "./data"
	return c
}

// Load reads the YAML file at path on top of the defaults, applies
// environment overrides, and validates the result. Unknown keys are an error,
// and so are the top-level sections that moved under features.
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}
	var root yaml.Node
	if err := yaml.Unmarshal(data, &root); err != nil {
		return nil, fmt.Errorf("parse config %s: %w", path, err)
	}
	if err := checkMovedKeys(&root); err != nil {
		return nil, fmt.Errorf("config %s: %w", path, err)
	}
	c := defaults()
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(c); err != nil && !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("parse config %s: %w", path, err)
	}
	if err := c.Features.fillEmpty(emptySections(&root, "features")); err != nil {
		return nil, fmt.Errorf("parse config %s: %w", path, err)
	}
	if c.LLM.MemoryModel == "" {
		c.LLM.MemoryModel = c.LLM.Model
	}
	c.applyEnv(os.LookupEnv)
	if err := c.Validate(); err != nil {
		return nil, fmt.Errorf("invalid config %s: %w", path, err)
	}
	return c, nil
}

// applyEnv applies environment variable overrides. lookup is injectable for tests.
func (c *Config) applyEnv(lookup func(string) (string, bool)) {
	if v, ok := lookup("HOOSHANG_TELEGRAM_TOKEN"); ok {
		c.Telegram.Token = v
	}
	if v, ok := lookup("HOOSHANG_LLM_API_KEY"); ok {
		c.LLM.APIKey = v
	}
}

// Validate rejects missing required values and out-of-range values.
func (c *Config) Validate() error {
	if c.Telegram.Token == "" {
		return fmt.Errorf("telegram.token is required (set it in the config or HOOSHANG_TELEGRAM_TOKEN)")
	}
	if len(c.Telegram.AdminIDs) == 0 {
		return fmt.Errorf("telegram.admin_ids must list at least one Telegram user ID")
	}
	for _, id := range c.Telegram.AdminIDs {
		if id <= 0 {
			return fmt.Errorf("telegram.admin_ids must all be > 0, got %d", id)
		}
	}
	if c.LLM.Model == "" {
		return fmt.Errorf("llm.model is required")
	}
	if c.LLM.Temperature < 0 || c.LLM.Temperature > 2 {
		return fmt.Errorf("llm.temperature must be in [0, 2], got %v", c.LLM.Temperature)
	}
	if c.LLM.MaxTokens < 1024 {
		return fmt.Errorf("llm.max_tokens must be >= 1024, got %d", c.LLM.MaxTokens)
	}
	if c.LLM.Timeout <= 0 {
		return fmt.Errorf("llm.timeout must be > 0")
	}
	if c.LLM.ContextTokens < c.LLM.MaxTokens+2048 {
		return fmt.Errorf("llm.context_tokens must be at least max_tokens + 2048, got %d", c.LLM.ContextTokens)
	}
	if c.LLM.EmbeddingModel != "" && c.LLM.EmbeddingDims < 1 {
		return fmt.Errorf("llm.embedding_dims must be >= 1, got %d", c.LLM.EmbeddingDims)
	}
	if c.Bot.HistoryMessages < 1 {
		return fmt.Errorf("bot.history_messages must be >= 1, got %d", c.Bot.HistoryMessages)
	}
	if c.Bot.HistoryWindow <= 0 {
		return fmt.Errorf("bot.history_window must be > 0")
	}
	if c.Bot.ProfileBudget < 0 || c.Bot.RandomProfiles < 0 {
		return fmt.Errorf("bot.profile_budget and bot.random_profiles must be >= 0")
	}
	if c.Bot.MessageRetention <= 0 {
		return fmt.Errorf("bot.message_retention must be > 0")
	}
	if c.Bot.SpontaneousChance < 0 || c.Bot.SpontaneousChance > 1 {
		return fmt.Errorf("bot.spontaneous_chance must be in [0, 1], got %v", c.Bot.SpontaneousChance)
	}
	if c.Bot.SpontaneousCooldown < 0 {
		return fmt.Errorf("bot.spontaneous_cooldown must be >= 0")
	}
	if c.Bot.MinMsgsBetween < 0 {
		return fmt.Errorf("bot.min_msgs_between must be >= 0")
	}
	for k, v := range c.Bot.Modes {
		switch k {
		case "joke", "tease", "flirt", "compliment", "comment", "sticker":
		default:
			return fmt.Errorf("bot.modes: unknown mode %q (allowed: joke, tease, flirt, compliment, comment, sticker)", k)
		}
		if v < 0 {
			return fmt.Errorf("bot.modes[%q] must be >= 0, got %d", k, v)
		}
	}
	if c.Bot.RateLimit.GlobalPerMinute < 1 {
		return fmt.Errorf("bot.rate_limit.global_per_minute must be >= 1, got %d", c.Bot.RateLimit.GlobalPerMinute)
	}
	if c.Bot.RateLimit.ChatPerMinute < 1 {
		return fmt.Errorf("bot.rate_limit.chat_per_minute must be >= 1, got %d", c.Bot.RateLimit.ChatPerMinute)
	}
	if c.Bot.ReactionChance < 0 || c.Bot.ReactionChance > 1 || c.Bot.ReactionCooldown < 0 {
		return fmt.Errorf("bot.reaction_chance must be in [0, 1] and reaction_cooldown >= 0")
	}
	h := c.Bot.Human
	if h.Typing.CharsPerSecond < 1 || h.Typing.Min < 0 || h.Typing.Max < h.Typing.Min {
		return fmt.Errorf("bot.human.typing: chars_per_second >= 1 and 0 <= min <= max")
	}
	if h.LateChance < 0 || h.LateChance > 1 || h.LateMax < 0 {
		return fmt.Errorf("bot.human.late_chance must be in [0, 1] and late_max >= 0")
	}
	if _, _, err := ParseQuietHours(h.QuietHours); err != nil {
		return fmt.Errorf("bot.human.quiet_hours: %w", err)
	}
	if h.Bubbles < 1 || h.Bubbles > 5 {
		return fmt.Errorf("bot.human.bubbles must be in [1, 5], got %d", h.Bubbles)
	}
	switch c.Nastaliq.Shaper {
	case "auto", "harfbuzz", "go":
	default:
		return fmt.Errorf("nastaliq.shaper must be auto, harfbuzz or go, got %q", c.Nastaliq.Shaper)
	}
	return c.Features.validate()
}

// ParseQuietHours parses "HH:MM-HH:MM" into minutes of the day; "" is no
// quiet hours (ok is false). The range may cross midnight.
func ParseQuietHours(s string) (from, to int, err error) {
	if s == "" {
		return 0, 0, nil
	}
	a, b, found := strings.Cut(s, "-")
	if !found {
		return 0, 0, fmt.Errorf("want HH:MM-HH:MM, got %q", s)
	}
	if from, err = parseHM(a); err != nil {
		return 0, 0, err
	}
	if to, err = parseHM(b); err != nil {
		return 0, 0, err
	}
	return from, to, nil
}

// parseHM parses "HH:MM" into minutes of the day.
func parseHM(s string) (int, error) {
	t, err := time.Parse("15:04", strings.TrimSpace(s))
	if err != nil {
		return 0, fmt.Errorf("want HH:MM, got %q", s)
	}
	return t.Hour()*60 + t.Minute(), nil
}
