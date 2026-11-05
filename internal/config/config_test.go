package config

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

// writeConfig writes content to a temp file and returns its path.
func writeConfig(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// minimal returns the smallest config that passes Validate.
const minimal = `
telegram:
  token: "1:FAKE"
  owner_id: 7
llm:
  model: "m"
`

func TestLoadAppliesDefaults(t *testing.T) {
	c, err := Load(writeConfig(t, minimal))
	if err != nil {
		t.Fatal(err)
	}
	// LLM defaults
	if c.LLM.BaseURL != "http://localhost:1234/v1" {
		t.Errorf("BaseURL = %q, want default", c.LLM.BaseURL)
	}
	if c.LLM.APIKey != "lm-studio" {
		t.Errorf("APIKey = %q, want default", c.LLM.APIKey)
	}
	if c.LLM.MemoryModel != "m" {
		t.Errorf("MemoryModel = %q, want = Model", c.LLM.MemoryModel)
	}
	if c.LLM.Temperature != 0.9 {
		t.Errorf("Temperature = %v, want 0.9", c.LLM.Temperature)
	}
	if c.LLM.MaxTokens != 2048 {
		t.Errorf("MaxTokens = %d, want 2048", c.LLM.MaxTokens)
	}
	if c.LLM.Timeout != 120*time.Second {
		t.Errorf("Timeout = %v, want 120s", c.LLM.Timeout)
	}
	if !c.LLM.Tools {
		t.Error("Tools = false, want default true")
	}
	// Bot defaults
	if c.Bot.Name != "هوشنگ" {
		t.Errorf("Name = %q", c.Bot.Name)
	}
	if len(c.Bot.Aliases) != 2 || c.Bot.Aliases[0] != "هوشی" || c.Bot.Aliases[1] != "hooshang" {
		t.Errorf("Aliases = %v", c.Bot.Aliases)
	}
	if c.Bot.Language != "fa" {
		t.Errorf("Language = %q", c.Bot.Language)
	}
	if c.Bot.HistoryMessages != 15 || c.Bot.HistoryWindow != 20*time.Minute || c.Bot.ProfileBudget != 3000 || c.Bot.RandomProfiles != 2 {
		t.Errorf("history/profile defaults = %d %v %v %v", c.Bot.HistoryMessages, c.Bot.HistoryWindow, c.Bot.ProfileBudget, c.Bot.RandomProfiles)
	}
	if c.Bot.MessageRetention != 72*time.Hour {
		t.Errorf("MessageRetention = %v", c.Bot.MessageRetention)
	}
	if c.Bot.SpontaneousChance != 0.07 {
		t.Errorf("SpontaneousChance = %v", c.Bot.SpontaneousChance)
	}
	if c.Bot.SpontaneousCooldown != 5*time.Minute {
		t.Errorf("SpontaneousCooldown = %v", c.Bot.SpontaneousCooldown)
	}
	if c.Bot.MinMsgsBetween != 8 {
		t.Errorf("MinMsgsBetween = %d", c.Bot.MinMsgsBetween)
	}
	wantModes := map[string]int{"joke": 3, "tease": 3, "flirt": 1, "compliment": 2, "comment": 2, "sticker": 1}
	if len(c.Bot.Modes) != len(wantModes) {
		t.Errorf("Modes = %v, want %v", c.Bot.Modes, wantModes)
	} else {
		for k, v := range wantModes {
			if c.Bot.Modes[k] != v {
				t.Errorf("Modes[%q] = %d, want %d", k, c.Bot.Modes[k], v)
			}
		}
	}
	if c.Bot.RateLimit.GlobalPerMinute != 10 || c.Bot.RateLimit.ChatPerMinute != 6 {
		t.Errorf("RateLimit = %+v", c.Bot.RateLimit)
	}
	// Every feature is off until its section is present.
	if on := c.Features.Active(); len(on) != 0 {
		t.Errorf("features on by default: %v", on)
	}
	if c.Nastaliq.Shaper != "auto" || c.Nastaliq.Font != "" {
		t.Errorf("Nastaliq = %+v", c.Nastaliq)
	}
	// Storage default
	if c.Storage.Path != "./data" {
		t.Errorf("Storage.Path = %q", c.Storage.Path)
	}
}

func TestLoadExplicitValues(t *testing.T) {
	content := `
telegram:
  token: "1:FAKE"
  owner_id: 7
llm:
  base_url: "https://openrouter.ai/api/v1"
  api_key: "sk"
  model: "big"
  memory_model: "small"
  temperature: 0.2
  max_tokens: 4096
  timeout: 5s
  tools: false
bot:
  name: "Ali"
  aliases: ["a", "b"]
  language: "en"
  history_messages: 5
  message_retention: 1h
  spontaneous_chance: 0.5
  spontaneous_cooldown: 1m
  min_msgs_between: 2
  modes:
    joke: 1
  rate_limit:
    global_per_minute: 1
    chat_per_minute: 1
features:
  profiles:
    update_every: 3
    max_age: 1h
    context_messages: 10
    max_items: 2
storage:
  path: "/var/lib/hooshang"
`
	c, err := Load(writeConfig(t, content))
	if err != nil {
		t.Fatal(err)
	}
	if c.LLM.BaseURL != "https://openrouter.ai/api/v1" || c.LLM.APIKey != "sk" ||
		c.LLM.MemoryModel != "small" || c.LLM.Temperature != 0.2 ||
		c.LLM.MaxTokens != 4096 || c.LLM.Timeout != 5*time.Second {
		t.Errorf("LLM = %+v", c.LLM)
	}
	if c.LLM.Tools {
		t.Error("Tools = true, want explicit false")
	}
	if c.Bot.Name != "Ali" || len(c.Bot.Aliases) != 2 || c.Bot.Language != "en" ||
		c.Bot.HistoryMessages != 5 || c.Bot.MessageRetention != time.Hour ||
		c.Bot.SpontaneousChance != 0.5 || c.Bot.SpontaneousCooldown != time.Minute ||
		c.Bot.MinMsgsBetween != 2 {
		t.Errorf("Bot = %+v", c.Bot)
	}
	// modes merges with the defaults: the one key from the file overrides,
	// the remaining keys keep their default weights.
	wantModes := map[string]int{"joke": 1, "tease": 3, "flirt": 1, "compliment": 2, "comment": 2, "sticker": 1}
	if len(c.Bot.Modes) != len(wantModes) {
		t.Errorf("Modes = %v, want merged %v", c.Bot.Modes, wantModes)
	} else {
		for k, v := range wantModes {
			if c.Bot.Modes[k] != v {
				t.Errorf("Modes[%q] = %d, want %d", k, c.Bot.Modes[k], v)
			}
		}
	}
	if c.Bot.RateLimit.GlobalPerMinute != 1 || c.Bot.RateLimit.ChatPerMinute != 1 {
		t.Errorf("RateLimit = %+v", c.Bot.RateLimit)
	}
	if p := c.Features.Profiles; !p.On() || p.UpdateEvery != 3 || p.MaxAge != time.Hour ||
		p.ContextMessages != 10 || p.MaxItems != 2 {
		t.Errorf("Profiles = %+v", p)
	}
	if c.Storage.Path != "/var/lib/hooshang" {
		t.Errorf("Storage.Path = %q", c.Storage.Path)
	}
}

func TestLoadExplicitZeros(t *testing.T) {
	content := `
telegram:
  token: "1:FAKE"
  owner_id: 7
llm:
  model: "m"
  temperature: 0
bot:
  spontaneous_chance: 0
`
	c, err := Load(writeConfig(t, content))
	if err != nil {
		t.Fatal(err)
	}
	if c.LLM.Temperature != 0 {
		t.Errorf("Temperature = %v, want explicit 0 (not the 0.9 default)", c.LLM.Temperature)
	}
	if c.Bot.SpontaneousChance != 0 {
		t.Errorf("SpontaneousChance = %v, want explicit 0 (not the 0.07 default)", c.Bot.SpontaneousChance)
	}
}

func TestLoadRateLimitZeroRejected(t *testing.T) {
	content := `
telegram:
  token: "1:FAKE"
  owner_id: 7
llm:
  model: "m"
bot:
  rate_limit:
    global_per_minute: 0
    chat_per_minute: 0
`
	if _, err := Load(writeConfig(t, content)); err == nil {
		t.Fatal("rate_limit 0: want error")
	}
}

func TestLoadEnvOverrides(t *testing.T) {
	if err := os.Setenv("HOOSHANG_TELEGRAM_TOKEN", "1:ENV"); err != nil {
		t.Fatal(err)
	}
	defer os.Unsetenv("HOOSHANG_TELEGRAM_TOKEN")
	if err := os.Setenv("HOOSHANG_LLM_API_KEY", "env-key"); err != nil {
		t.Fatal(err)
	}
	defer os.Unsetenv("HOOSHANG_LLM_API_KEY")

	c, err := Load(writeConfig(t, minimal))
	if err != nil {
		t.Fatal(err)
	}
	if c.Telegram.Token != "1:ENV" {
		t.Errorf("Token = %q, want env override", c.Telegram.Token)
	}
	if c.LLM.APIKey != "env-key" {
		t.Errorf("APIKey = %q, want env override", c.LLM.APIKey)
	}
}

func TestLoadFileMissing(t *testing.T) {
	if _, err := Load(filepath.Join(t.TempDir(), "nope.yaml")); err == nil {
		t.Fatal("want error for missing file")
	}
}

func TestLoadBadYAML(t *testing.T) {
	if _, err := Load(writeConfig(t, "telegram: [unclosed")); err == nil {
		t.Fatal("want error for bad YAML")
	}
}

func TestValidateRequired(t *testing.T) {
	valid := &Config{}
	applyDefaultsForTest(valid)

	valid.Telegram.Token = "1:FAKE"
	valid.Telegram.OwnerID = 7
	valid.LLM.Model = "m"
	if err := valid.Validate(); err != nil {
		t.Fatalf("complete config: %v", err)
	}

	c := clone(valid)
	c.Telegram.Token = ""
	if err := c.Validate(); err == nil {
		t.Error("missing token: want error")
	}

	c = clone(valid)
	c.Telegram.OwnerID = 0
	if err := c.Validate(); err == nil {
		t.Error("missing owner_id: want error")
	}
	c.Telegram.OwnerID = -3
	if err := c.Validate(); err == nil {
		t.Error("negative owner_id: want error")
	}

	c = clone(valid)
	c.LLM.Model = ""
	if err := c.Validate(); err == nil {
		t.Error("missing model: want error")
	}
}

func TestValidateBadRanges(t *testing.T) {
	valid := &Config{}
	applyDefaultsForTest(valid)
	valid.Telegram.Token = "1:FAKE"
	valid.Telegram.OwnerID = 7
	valid.LLM.Model = "m"

	c := clone(valid)
	c.LLM.MaxTokens = 512
	if err := c.Validate(); err == nil {
		t.Error("max_tokens 512: want error")
	}

	c = clone(valid)
	c.LLM.Temperature = 3
	if err := c.Validate(); err == nil {
		t.Error("temperature 3: want error")
	}

	c = clone(valid)
	c.LLM.Timeout = 0
	if err := c.Validate(); err == nil {
		t.Error("timeout 0: want error")
	}

	c = clone(valid)
	c.Bot.SpontaneousChance = 1.5
	if err := c.Validate(); err == nil {
		t.Error("spontaneous_chance 1.5: want error")
	}

	c = clone(valid)
	c.Bot.Modes["dance"] = 1
	if err := c.Validate(); err == nil {
		t.Error("unknown mode key: want error")
	}

	c = clone(valid)
	c.Bot.RateLimit.ChatPerMinute = -1
	if err := c.Validate(); err == nil {
		t.Error("negative chat cap: want error")
	}

	c = clone(valid)
	c.Features.Profiles = &ProfilesFeature{Enabled: true, UpdateEvery: 15, MaxAge: time.Hour, ContextMessages: 150, MaxItems: -1}
	if err := c.Validate(); err == nil {
		t.Error("negative max_items: want error")
	}
}

func TestLoadExampleFile(t *testing.T) {
	// The bundled example must parse with every feature on, and after an env
	// token it must validate. Its model file is git-ignored: point it at a stand-in.
	t.Setenv("HOOSHANG_TELEGRAM_TOKEN", "1:FAKE")
	example, err := os.ReadFile("../../config.example.yaml")
	if err != nil {
		t.Fatal(err)
	}
	model := filepath.Join(t.TempDir(), "model.bin")
	if err := os.WriteFile(model, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	yamlText := strings.Replace(string(example), "models/ggml-large-v3-turbo-q5_0.bin", model, 1)
	if yamlText == string(example) {
		t.Fatal("the example no longer names models/ggml-large-v3-turbo-q5_0.bin")
	}
	c, err := Load(writeConfig(t, yamlText))
	if err != nil {
		t.Fatal(err)
	}
	if c.LLM.MaxTokens != 4096 || c.Bot.HistoryMessages != 15 || c.Bot.HistoryWindow != 20*time.Minute || c.Bot.Name != "هوشنگ" {
		t.Errorf("example values off: %+v / %+v", c.LLM, c.Bot)
	}
	want := []string{"profiles", "group_memory", "glossary", "media", "faal", "poem", "roast", "voice"}
	if got := c.Features.Active(); !reflect.DeepEqual(got, want) {
		t.Errorf("features on in the example = %v, want %v", got, want)
	}
}

// applyDefaultsForTest fills defaults for a hand-built Config (tests only).
func applyDefaultsForTest(c *Config) { *c = *defaults() }

func clone(c *Config) *Config {
	c2 := *c
	c2.Bot.Modes = make(map[string]int, len(c.Bot.Modes))
	for k, v := range c.Bot.Modes {
		c2.Bot.Modes[k] = v
	}
	return &c2
}

func TestProfileBudgetRange(t *testing.T) {
	if _, err := Load(writeConfig(t, minimal+"bot:\n  profile_budget: -1\n")); err == nil {
		t.Fatal("profile_budget -1 accepted")
	}
	c, err := Load(writeConfig(t, minimal+"bot:\n  random_profiles: 0\n"))
	if err != nil || c.Bot.RandomProfiles != 0 {
		t.Fatalf("random_profiles 0: %v %v", c, err)
	}
}

func TestGroupMemoryDefaultsAndValidation(t *testing.T) {
	c, err := Load(writeConfig(t, minimal+"features:\n  group_memory:\n"))
	if err != nil {
		t.Fatal(err)
	}
	g := c.Features.GroupMemory
	if !g.On() || g.TTL != 7*24*time.Hour || g.MaxItems != 15 || g.UpdateEvery != 50 || g.InPrompt != 4 || g.PromptChance != 0.3 {
		t.Fatalf("defaults = %+v", g)
	}
	if _, err := Load(writeConfig(t, minimal+"features:\n  group_memory:\n    ttl: 0s\n")); err == nil {
		t.Fatal("ttl 0 accepted")
	}
	if _, err := Load(writeConfig(t, minimal+"features:\n  group_memory:\n    enabled: false\n    ttl: 0s\n")); err != nil {
		t.Fatalf("a disabled group memory is not validated: %v", err)
	}
}

func TestShaperIsValidated(t *testing.T) {
	if _, err := Load(writeConfig(t, minimal+"nastaliq:\n  shaper: harfbuz\n")); err == nil {
		t.Fatal("shaper 'harfbuz' accepted")
	}
}

func TestMediaFramesBounds(t *testing.T) {
	c, err := Load(writeConfig(t, minimal+"features:\n  media:\n"))
	if err != nil || c.Features.Media.Frames != 3 || c.Features.Media.FFmpeg != "" || !c.Features.Media.Send {
		t.Fatalf("defaults: %+v, err %v", c.Features.Media, err)
	}
	for _, tc := range []struct {
		yaml string
		ok   bool
	}{
		{"media:\n    frames: 1\n", true},
		{"media:\n    frames: 8\n", true},
		{"media:\n    frames: 0\n", false},
		{"media:\n    frames: 9\n", false},
		{"media:\n    frames: -1\n", false},
		{"media:\n    enabled: false\n    frames: 0\n", true},
		{"media:\n    ffmpeg: /nonexistent/ffmpeg\n", true}, // existence is not checked: the bot runs without it
	} {
		if _, err := Load(writeConfig(t, minimal+"features:\n  "+tc.yaml)); (err == nil) != tc.ok {
			t.Errorf("%q: err = %v, want ok=%v", tc.yaml, err, tc.ok)
		}
	}
}
