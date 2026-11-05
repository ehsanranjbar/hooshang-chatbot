package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestAbsentSectionIsOff(t *testing.T) {
	c, err := Load(writeConfig(t, minimal+"features:\n  glossary:\n"))
	if err != nil {
		t.Fatal(err)
	}
	if !c.Features.Glossary.On() {
		t.Error("glossary is present but off")
	}
	if c.Features.Faal != nil || c.Features.Faal.On() || c.Features.Voice.On() || c.Features.Media.On() {
		t.Errorf("absent sections are on: %v", c.Features.Active())
	}
	if got := c.Features.Active(); len(got) != 1 || got[0] != "glossary" {
		t.Errorf("Active = %v", got)
	}
}

func TestPresentSectionWithEnabledFalseIsOff(t *testing.T) {
	c, err := Load(writeConfig(t, minimal+"features:\n  faal:\n    enabled: false\n    image: false\n"))
	if err != nil {
		t.Fatal(err)
	}
	if c.Features.Faal == nil {
		t.Fatal("the section is present: the settings are kept")
	}
	if c.Features.Faal.On() || c.Features.Faal.Image {
		t.Errorf("faal = %+v, want off with its image setting kept", c.Features.Faal)
	}
}

func TestPartialSectionFillsDefaults(t *testing.T) {
	c, err := Load(writeConfig(t, minimal+"features:\n  profiles:\n    update_every: 3\n  poem:\n    image: false\n  group_memory:\n    in_prompt: 0\n"))
	if err != nil {
		t.Fatal(err)
	}
	p := c.Features.Profiles
	if !p.On() || p.UpdateEvery != 3 || p.MaxAge != 24*time.Hour || p.ContextMessages != 150 || p.MaxItems != 10 {
		t.Errorf("profiles = %+v", p)
	}
	if poem := c.Features.Poem; !poem.On() || poem.Image {
		t.Errorf("poem = %+v: on, but the explicit image: false must stay", poem)
	}
	if g := c.Features.GroupMemory; !g.On() || g.InPrompt != 0 || g.TTL != 7*24*time.Hour {
		t.Errorf("an explicit 0 must stay 0 and the rest default: %+v", g)
	}
}

func TestEmptySectionIsOnWithDefaults(t *testing.T) {
	c, err := Load(writeConfig(t, minimal+"features:\n  roast:\n  media:\n"))
	if err != nil {
		t.Fatal(err)
	}
	if !c.Features.Roast.On() || !c.Features.Media.On() || c.Features.Media.Frames != 3 {
		t.Errorf("empty sections: %v", c.Features.Active())
	}
}

func TestOldTopLevelSectionsAreRejected(t *testing.T) {
	_, err := Load(writeConfig(t, minimal+"memory:\n  update_every: 3\nfaal:\n  image: true\nmedia:\n  words: 20\ngroup_memory:\n  ttl: 1h\nglossary:\n  max_items: 5\n"))
	if err == nil {
		t.Fatal("old sections accepted")
	}
	for _, want := range []string{"features:", `"memory"`, "features.profiles", `"group_memory"`, "features.group_memory",
		`"glossary"`, "features.glossary", `"media"`, "features.media", `"faal"`, "features.faal", "features.poem", "nastaliq"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the error does not mention %q: %v", want, err)
		}
	}
}

func TestUnknownKeysAreRejected(t *testing.T) {
	for _, extra := range []string{
		"features:\n  faal:\n    imagee: true\n",
		"features:\n  hafez:\n",
		"botz:\n  name: x\n",
		"llm:\n  modle: x\n",
	} {
		if _, err := Load(writeConfig(t, minimal+extra)); err == nil {
			t.Errorf("%q accepted", extra)
		}
	}
	_, err := Load(writeConfig(t, minimal+"features:\n  faal:\n    imagee: true\n"))
	if err == nil || !strings.Contains(err.Error(), `features.faal has no key "imagee"`) {
		t.Errorf("error = %v", err)
	}
}

func TestVoiceValidation(t *testing.T) {
	dir := t.TempDir()
	model := filepath.Join(dir, "ggml-large-v3-turbo-q5_0.bin")
	english := filepath.Join(dir, "ggml-base.en.bin")
	for _, p := range []string{model, english} {
		if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	for _, tc := range []struct {
		name, voice string
		ok          bool
	}{
		{"model exists", "model: " + model, true},
		{"defaults", "model: " + model + "\n    language: fa", true},
		{"no model", "language: fa", false},
		{"missing model file", "model: " + filepath.Join(dir, "nope.bin"), false},
		{"a directory", "model: " + dir, false},
		{"english-only model for Persian", "model: " + english, false},
		{"english-only model for English", "model: " + english + "\n    language: en", true},
		{"max_duration 0", "model: " + model + "\n    max_duration: 0s", false},
		{"max_queue 0", "model: " + model + "\n    max_queue: 0", false},
		{"max_words 0", "model: " + model + "\n    max_words: 0", false},
		{"negative threads", "model: " + model + "\n    threads: -1", false},
		{"disabled is not validated", "enabled: false\n    max_queue: 0", true},
	} {
		c, err := Load(writeConfig(t, minimal+"features:\n  voice:\n    "+tc.voice+"\n"))
		if (err == nil) != tc.ok {
			t.Errorf("%s: err = %v, want ok=%v", tc.name, err, tc.ok)
		}
		if err == nil && tc.name == "model exists" {
			v := c.Features.Voice
			if !v.On() || v.Language != "fa" || v.MaxDuration != 120*time.Second || v.MaxQueue != 10 || v.MaxWords != 120 || !v.VideoNotes || v.Threads != 0 {
				t.Errorf("voice defaults = %+v", v)
			}
		}
	}
}

func TestFFmpegFromMedia(t *testing.T) {
	c := &Config{}
	if c.FFmpeg() != "" {
		t.Error("no media section: PATH")
	}
	c.Features.Media = &MediaFeature{FFmpeg: "/opt/ffmpeg"}
	if c.FFmpeg() != "/opt/ffmpeg" {
		t.Errorf("FFmpeg = %q: a path is kept even when media is off", c.FFmpeg())
	}
}
