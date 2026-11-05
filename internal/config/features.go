package config

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Features are the optional capabilities of the bot, under `features:`.
//
// A feature is on when its section is present in the file, and off when the
// section is absent (or commented out). Inside a present section,
// `enabled: false` turns it off too, so its settings can be kept. A nil
// pointer is an absent section. Always ask a feature with On(), which is safe
// on nil; read its other fields only after that.
//
// A present section starts from the feature's defaults: keys the file leaves
// out keep their default and an explicit 0 stays 0. An empty section
// (`roast:`) is on with all defaults.
type Features struct {
	Profiles    *ProfilesFeature    `yaml:"profiles"`     // per-member memory
	GroupMemory *GroupMemoryFeature `yaml:"group_memory"` // what the group cared about
	Glossary    *GlossaryFeature    `yaml:"glossary"`     // the group's own words
	Media       *MediaFeature       `yaml:"media"`        // describing and sending photos, stickers, GIFs
	Faal        *FaalFeature        `yaml:"faal"`         // /faal and the hafez_faal tool
	Poem        *PoemFeature        `yaml:"poem"`         // /poem and the random_poem tool
	Roast       *RoastFeature       `yaml:"roast"`        // /roast
	Voice       *VoiceFeature       `yaml:"voice"`        // transcribing voice messages
	Greetings   *GreetingsFeature   `yaml:"greetings"`    // answering صبح/شب بخیر, welcoming and seeing off members
	Moderation  *ModerationFeature  `yaml:"moderation"`   // the admin moderates in plain words
}

// GreetingsFeature is features.greetings: the bot sometimes answers a
// member's «صبح بخیر» / «شب بخیر», welcomes new members and comments when
// someone leaves.
type GreetingsFeature struct {
	Enabled        bool          `yaml:"enabled"`
	Chance         float64       `yaml:"chance"`          // chance of answering a greeting, [0,1]; default 0.6
	MemberCooldown time.Duration `yaml:"member_cooldown"` // don't greet the same member again within; default 12h
	ChatCooldown   time.Duration `yaml:"chat_cooldown"`   // don't greet anyone in the chat again within; default 30m
	Welcome        bool          `yaml:"welcome"`         // greet new members; default true
	Farewell       bool          `yaml:"farewell"`        // comment when a member leaves; default true
}

// On reports whether the feature is on: its section is present and not disabled.
func (f *GreetingsFeature) On() bool { return f != nil && f.Enabled }

// UnmarshalYAML fills the defaults, then the keys of the section.
func (f *GreetingsFeature) UnmarshalYAML(n *yaml.Node) error {
	type plain GreetingsFeature
	*f = GreetingsFeature{Enabled: true, Chance: 0.6, MemberCooldown: 12 * time.Hour, ChatCooldown: 30 * time.Minute, Welcome: true, Farewell: true}
	return decodeFeature(n, "greetings", (*plain)(f))
}

// ProfilesFeature is features.profiles: what the bot remembers about each member.
type ProfilesFeature struct {
	Enabled         bool          `yaml:"enabled"`
	UpdateEvery     int           `yaml:"update_every"`     // update a profile every N new messages; default 15
	MaxAge          time.Duration `yaml:"max_age"`          // older profiles are re-checked; default 24h
	ContextMessages int           `yaml:"context_messages"` // messages the updater sees; default 150
	MaxItems        int           `yaml:"max_items"`        // per profile section; default 10
}

// GroupMemoryFeature is features.group_memory: the events the group cared about.
type GroupMemoryFeature struct {
	Enabled      bool          `yaml:"enabled"`
	TTL          time.Duration `yaml:"ttl"`           // an event is forgotten this long after it was last seen; default 168h
	MaxItems     int           `yaml:"max_items"`     // kept per group, highest engagement first; default 15
	UpdateEvery  int           `yaml:"update_every"`  // group messages between updates; default 50
	InPrompt     int           `yaml:"in_prompt"`     // events shown in a prompt; default 4
	PromptChance float64       `yaml:"prompt_chance"` // chance they are shown in a reply or spontaneous message; default 0.3
}

// GlossaryFeature is features.glossary: the group's own words; it also gates
// /terms and the learn_term and forget_term tools.
type GlossaryFeature struct {
	Enabled  bool `yaml:"enabled"`
	MaxItems int  `yaml:"max_items"` // terms kept per group; the least recently used is dropped; default 100
	InPrompt int  `yaml:"in_prompt"` // terms that appear in the conversation shown to the model; default 8
}

// MediaFeature is features.media: describing photos, stickers and GIFs with
// the (vision) model, and answering with a sticker or GIF.
type MediaFeature struct {
	Enabled  bool          `yaml:"enabled"`
	Photos   bool          `yaml:"photos"`    // default true
	Stickers bool          `yaml:"stickers"`  // default true
	GIFs     bool          `yaml:"gifs"`      // default true
	Send     bool          `yaml:"send"`      // the find_media and send_media tools; default true
	Words    int           `yaml:"words"`     // description length in words; default 30
	CacheTTL time.Duration `yaml:"cache_ttl"` // how long sticker/GIF descriptions are reused; default 2160h (90 days)
	MaxQueue int           `yaml:"max_queue"` // pending descriptions; more are skipped; default 20
	Frames   int           `yaml:"frames"`    // frames sent for an animated sticker or GIF (1 = thumbnail only); needs ffmpeg; default 3
	FFmpeg   string        `yaml:"ffmpeg"`    // path to ffmpeg, also used by voice; empty = look it up in PATH
}

// FaalFeature is features.faal: /faal, the hafez_faal tool and the «فال» phrase.
type FaalFeature struct {
	Enabled bool `yaml:"enabled"`
	Image   bool `yaml:"image"` // send the ghazal as a Nastaliq image; default true
}

// PoemFeature is features.poem: /poem and the random_poem tool.
type PoemFeature struct {
	Enabled bool `yaml:"enabled"`
	Image   bool `yaml:"image"` // send the بیت as a Nastaliq image; default true
}

// RoastFeature is features.roast: /roast.
type RoastFeature struct {
	Enabled bool `yaml:"enabled"`
}

// VoiceFeature is features.voice: voice messages and video notes are
// transcribed locally with whisper.cpp.
type VoiceFeature struct {
	Enabled     bool          `yaml:"enabled"`
	Whisper     string        `yaml:"whisper"`      // path to whisper-cli; empty = look it up in PATH
	Model       string        `yaml:"model"`        // a multilingual ggml model file; required
	Language    string        `yaml:"language"`     // spoken language; default "fa"
	Threads     int           `yaml:"threads"`      // 0 = whisper's default
	MaxDuration time.Duration `yaml:"max_duration"` // longer voices are not transcribed; default 120s
	MaxQueue    int           `yaml:"max_queue"`    // pending transcriptions; more are skipped; default 10
	MaxWords    int           `yaml:"max_words"`    // transcript length cap in words; default 120
	VideoNotes  bool          `yaml:"video_notes"`  // also transcribe round video messages; default true
}

// ModerationActions are the actions the moderate tool knows, in a fixed order.
var ModerationActions = []string{"delete", "mute", "kick", "ban", "unban", "pin", "unpin"}

// ModerationFeature is features.moderation: the admin tells the bot in plain
// words to delete, mute, kick, ban, unban, pin or unpin, and it acts at once.
type ModerationFeature struct {
	Enabled     bool          `yaml:"enabled"`
	Actions     []string      `yaml:"actions"`      // what the bot may do; default all of ModerationActions
	DefaultMute time.Duration `yaml:"default_mute"` // mute length when none is given; default 1h
	MaxMute     time.Duration `yaml:"max_mute"`     // longest mute; default 168h
	MaxDelete   int           `yaml:"max_delete"`   // most messages deleted in one request; default 20
}

// On reports whether the feature is on: its section is present and not disabled.
func (f *ProfilesFeature) On() bool { return f != nil && f.Enabled }

// On reports whether the feature is on: its section is present and not disabled.
func (f *GroupMemoryFeature) On() bool { return f != nil && f.Enabled }

// On reports whether the feature is on: its section is present and not disabled.
func (f *GlossaryFeature) On() bool { return f != nil && f.Enabled }

// On reports whether the feature is on: its section is present and not disabled.
func (f *MediaFeature) On() bool { return f != nil && f.Enabled }

// On reports whether the feature is on: its section is present and not disabled.
func (f *FaalFeature) On() bool { return f != nil && f.Enabled }

// On reports whether the feature is on: its section is present and not disabled.
func (f *PoemFeature) On() bool { return f != nil && f.Enabled }

// On reports whether the feature is on: its section is present and not disabled.
func (f *RoastFeature) On() bool { return f != nil && f.Enabled }

// On reports whether the feature is on: its section is present and not disabled.
func (f *VoiceFeature) On() bool { return f != nil && f.Enabled }

// On reports whether the feature is on: its section is present and not disabled.
func (f *ModerationFeature) On() bool { return f != nil && f.Enabled }

// UnmarshalYAML fills the defaults, then the keys of the section.
func (f *ModerationFeature) UnmarshalYAML(n *yaml.Node) error {
	type plain ModerationFeature
	*f = ModerationFeature{Enabled: true, Actions: append([]string(nil), ModerationActions...),
		DefaultMute: time.Hour, MaxMute: 168 * time.Hour, MaxDelete: 20}
	return decodeFeature(n, "moderation", (*plain)(f))
}

// UnmarshalYAML fills the defaults, then the keys of the section.
func (f *ProfilesFeature) UnmarshalYAML(n *yaml.Node) error {
	type plain ProfilesFeature // no methods: decoding into f itself would recurse
	*f = ProfilesFeature{Enabled: true, UpdateEvery: 15, MaxAge: 24 * time.Hour, ContextMessages: 150, MaxItems: 10}
	return decodeFeature(n, "profiles", (*plain)(f))
}

// UnmarshalYAML fills the defaults, then the keys of the section.
func (f *GroupMemoryFeature) UnmarshalYAML(n *yaml.Node) error {
	type plain GroupMemoryFeature
	*f = GroupMemoryFeature{Enabled: true, TTL: 7 * 24 * time.Hour, MaxItems: 15, UpdateEvery: 50, InPrompt: 4, PromptChance: 0.3}
	return decodeFeature(n, "group_memory", (*plain)(f))
}

// UnmarshalYAML fills the defaults, then the keys of the section.
func (f *GlossaryFeature) UnmarshalYAML(n *yaml.Node) error {
	type plain GlossaryFeature
	*f = GlossaryFeature{Enabled: true, MaxItems: 100, InPrompt: 8}
	return decodeFeature(n, "glossary", (*plain)(f))
}

// UnmarshalYAML fills the defaults, then the keys of the section.
func (f *MediaFeature) UnmarshalYAML(n *yaml.Node) error {
	type plain MediaFeature
	*f = MediaFeature{Enabled: true, Photos: true, Stickers: true, GIFs: true, Send: true,
		Words: 30, CacheTTL: 90 * 24 * time.Hour, MaxQueue: 20, Frames: 3}
	return decodeFeature(n, "media", (*plain)(f))
}

// UnmarshalYAML fills the defaults, then the keys of the section.
func (f *FaalFeature) UnmarshalYAML(n *yaml.Node) error {
	type plain FaalFeature
	*f = FaalFeature{Enabled: true, Image: true}
	return decodeFeature(n, "faal", (*plain)(f))
}

// UnmarshalYAML fills the defaults, then the keys of the section.
func (f *PoemFeature) UnmarshalYAML(n *yaml.Node) error {
	type plain PoemFeature
	*f = PoemFeature{Enabled: true, Image: true}
	return decodeFeature(n, "poem", (*plain)(f))
}

// UnmarshalYAML fills the defaults, then the keys of the section.
func (f *RoastFeature) UnmarshalYAML(n *yaml.Node) error {
	type plain RoastFeature
	*f = RoastFeature{Enabled: true}
	return decodeFeature(n, "roast", (*plain)(f))
}

// UnmarshalYAML fills the defaults, then the keys of the section.
func (f *VoiceFeature) UnmarshalYAML(n *yaml.Node) error {
	type plain VoiceFeature
	*f = VoiceFeature{Enabled: true, Language: "fa", MaxDuration: 120 * time.Second, MaxQueue: 10, MaxWords: 120, VideoNotes: true}
	return decodeFeature(n, "voice", (*plain)(f))
}

// decodeFeature decodes a feature's mapping node into dst (a pointer to a
// method-less copy of the feature struct), rejecting keys it does not have:
// yaml's KnownFields does not reach into a custom unmarshaler.
func decodeFeature(n *yaml.Node, name string, dst any) error {
	if n.Kind != yaml.MappingNode {
		return fmt.Errorf("line %d: features.%s must be a section of keys", n.Line, name)
	}
	known := yamlKeys(reflect.TypeOf(dst).Elem())
	for i := 0; i+1 < len(n.Content); i += 2 {
		if k := n.Content[i]; !contains(known, k.Value) {
			return fmt.Errorf("line %d: features.%s has no key %q (keys: %s)", k.Line, name, k.Value, strings.Join(known, ", "))
		}
	}
	return n.Decode(dst)
}

// yamlKeys lists the yaml key names of a struct type, sorted.
func yamlKeys(t reflect.Type) []string {
	var keys []string
	for i := range t.NumField() {
		if name, _, _ := strings.Cut(t.Field(i).Tag.Get("yaml"), ","); name != "" && name != "-" {
			keys = append(keys, name)
		}
	}
	sort.Strings(keys)
	return keys
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// movedKeys are the top-level sections that now live under features (or in
// nastaliq), with where each went.
var movedKeys = map[string]string{
	"memory":       "features.profiles",
	"group_memory": "features.group_memory",
	"glossary":     "features.glossary",
	"media":        "features.media",
	"faal":         "features.faal (image), features.poem (was faal.poem_images) and nastaliq (was faal.font and faal.shaper)",
}

// checkMovedKeys returns one error naming every old top-level section found
// in the document and where it moved.
func checkMovedKeys(root *yaml.Node) error {
	top := topMapping(root)
	if top == nil {
		return nil
	}
	var old []string
	for i := 0; i+1 < len(top.Content); i += 2 {
		if to, ok := movedKeys[top.Content[i].Value]; ok {
			old = append(old, fmt.Sprintf("%q (line %d) is now %s", top.Content[i].Value, top.Content[i].Line, to))
		}
	}
	if len(old) == 0 {
		return nil
	}
	return fmt.Errorf("the optional sections moved under `features:` (a feature is on when its section is present): %s", strings.Join(old, "; "))
}

// topMapping returns the document's top-level mapping, or nil.
func topMapping(root *yaml.Node) *yaml.Node {
	if root.Kind != yaml.DocumentNode || len(root.Content) == 0 || root.Content[0].Kind != yaml.MappingNode {
		return nil
	}
	return root.Content[0]
}

// emptySections returns the keys of section's mapping that have no value
// (`roast:`): yaml decodes those to a nil pointer, but a present section means on.
func emptySections(root *yaml.Node, section string) []string {
	top := topMapping(root)
	if top == nil {
		return nil
	}
	var names []string
	for i := 0; i+1 < len(top.Content); i += 2 {
		sec := top.Content[i+1]
		if top.Content[i].Value != section || sec.Kind != yaml.MappingNode {
			continue
		}
		for k := 0; k+1 < len(sec.Content); k += 2 {
			if v := sec.Content[k+1]; v.Kind == yaml.ScalarNode && v.ShortTag() == "!!null" {
				names = append(names, sec.Content[k].Value)
			}
		}
	}
	return names
}

// fillEmpty turns the named empty sections on with their defaults.
func (f *Features) fillEmpty(names []string) error {
	v := reflect.ValueOf(f).Elem()
	empty := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
	for i := range v.NumField() {
		name, _, _ := strings.Cut(v.Type().Field(i).Tag.Get("yaml"), ",")
		if !contains(names, name) {
			continue
		}
		p := reflect.New(v.Field(i).Type().Elem())
		if err := p.Interface().(yaml.Unmarshaler).UnmarshalYAML(empty); err != nil {
			return err
		}
		v.Field(i).Set(p)
	}
	return nil
}

// Active lists the names of the features that are on, in a fixed order.
func (f Features) Active() []string {
	var on []string
	for _, e := range []struct {
		name string
		on   bool
	}{
		{"profiles", f.Profiles.On()}, {"group_memory", f.GroupMemory.On()}, {"glossary", f.Glossary.On()},
		{"media", f.Media.On()}, {"faal", f.Faal.On()}, {"poem", f.Poem.On()},
		{"roast", f.Roast.On()}, {"voice", f.Voice.On()}, {"greetings", f.Greetings.On()},
		{"moderation", f.Moderation.On()},
	} {
		if e.on {
			on = append(on, e.name)
		}
	}
	return on
}

// FFmpeg is the ffmpeg to run: features.media.ffmpeg when set (even if media
// is off, so the path can be kept), else "" for a lookup in PATH.
func (c *Config) FFmpeg() string {
	if c.Features.Media != nil {
		return c.Features.Media.FFmpeg
	}
	return ""
}

// validate checks the features that are on; a disabled one keeps whatever it has.
func (f *Features) validate() error {
	if p := f.Profiles; p.On() {
		if p.UpdateEvery < 1 || p.MaxAge <= 0 || p.ContextMessages < 1 || p.MaxItems < 1 {
			return fmt.Errorf("features.profiles: update_every, context_messages and max_items must be >= 1 and max_age > 0")
		}
	}
	if g := f.GroupMemory; g.On() && (g.TTL <= 0 || g.MaxItems < 1 || g.UpdateEvery < 1 || g.InPrompt < 0 || g.PromptChance < 0 || g.PromptChance > 1) {
		return fmt.Errorf("features.group_memory: ttl must be > 0, max_items and update_every >= 1, in_prompt >= 0, prompt_chance in [0, 1]")
	}
	if g := f.Glossary; g.On() && (g.MaxItems < 1 || g.InPrompt < 1) {
		return fmt.Errorf("features.glossary: max_items and in_prompt must be >= 1")
	}
	if g := f.Greetings; g.On() && (g.Chance < 0 || g.Chance > 1 || g.MemberCooldown < 0 || g.ChatCooldown < 0) {
		return fmt.Errorf("features.greetings: chance must be in [0, 1] and the cooldowns >= 0")
	}
	if m := f.Media; m.On() {
		if m.Words < 5 || m.Words > 200 || m.CacheTTL <= 0 || m.MaxQueue < 1 {
			return fmt.Errorf("features.media: words must be in [5, 200], cache_ttl > 0, max_queue >= 1")
		}
		if m.Frames < 1 || m.Frames > 8 {
			return fmt.Errorf("features.media: frames must be in [1, 8]")
		}
	}
	if err := f.Moderation.validate(); err != nil {
		return err
	}
	return f.Voice.validate()
}

// validate checks the moderation settings of a feature that is on.
func (m *ModerationFeature) validate() error {
	if !m.On() {
		return nil
	}
	for _, a := range m.Actions {
		if !contains(ModerationActions, a) {
			return fmt.Errorf("features.moderation.actions: unknown action %q (allowed: %s)", a, strings.Join(ModerationActions, ", "))
		}
	}
	if m.DefaultMute <= 0 || m.MaxMute <= 0 || m.DefaultMute > m.MaxMute {
		return fmt.Errorf("features.moderation: default_mute and max_mute must be > 0 and default_mute <= max_mute")
	}
	if m.MaxDelete < 1 || m.MaxDelete > 100 {
		return fmt.Errorf("features.moderation: max_delete must be in [1, 100]")
	}
	return nil
}

// enModel matches the file name of an English-only whisper model (ggml-base.en.bin).
var enModel = regexp.MustCompile(`\.en([.\-_]|$)`)

// validate checks the voice settings of a feature that is on; the model file
// must exist (whisper itself is looked up at startup, like ffmpeg).
func (v *VoiceFeature) validate() error {
	if !v.On() {
		return nil
	}
	if v.Model == "" {
		return fmt.Errorf("features.voice.model is required: the path of a multilingual whisper.cpp ggml model")
	}
	if st, err := os.Stat(v.Model); err != nil || st.IsDir() {
		return fmt.Errorf("features.voice.model %q is not a readable file", v.Model)
	}
	if enModel.MatchString(filepath.Base(v.Model)) && v.Language != "en" {
		return fmt.Errorf("features.voice.model %q is English-only: Persian needs a multilingual model", v.Model)
	}
	if v.MaxDuration <= 0 || v.MaxQueue < 1 || v.MaxWords < 1 || v.Threads < 0 || v.Language == "" {
		return fmt.Errorf("features.voice: max_duration must be > 0, max_queue and max_words >= 1, threads >= 0, language not empty")
	}
	return nil
}
