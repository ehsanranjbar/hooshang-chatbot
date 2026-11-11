package bot

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/mymmrac/telego"

	"github.com/ehsanranjbar/hooshang/internal/config"
	"github.com/ehsanranjbar/hooshang/internal/i18n"
	"github.com/ehsanranjbar/hooshang/internal/llm"
	"github.com/ehsanranjbar/hooshang/internal/memory"
	"github.com/ehsanranjbar/hooshang/internal/store"
)

// command sends a command from user as a message of testChat and waits for
// the bot's single answer.
func command(t *testing.T, b *Bot, tg *fakeTG, from *telego.User, text string) string {
	t.Helper()
	before := tg.sentCount()
	b.handleMessage(context.Background(), mediaMsg(int(time.Now().UnixNano()%1e6), from, func(m *telego.Message) { m.Text = text }))
	eventually(t, "the answer to "+text, func() bool { return tg.sentCount() > before })
	return tg.sentTexts()[before]
}

func TestSwitchedOffCommandsSayTheyAreOff(t *testing.T) {
	b, _, tg, fake := replyBot(t, 100)
	f := &b.deps.Cfg.Features
	f.Faal, f.Poem, f.Roast, f.Profiles, f.Glossary = nil, &config.PoemFeature{Enabled: false}, nil, nil, nil
	for _, cmd := range []string{"/faal", "/poem", "/roast", "/me", "/forgetme", "/terms"} {
		if got := command(t, b, tg, boss, cmd); got != i18n.FeatureOff {
			t.Errorf("%s answered %q, want the feature-off note", cmd, got)
		}
	}
	if len(fake.Calls) != 0 {
		t.Errorf("a switched-off command called the model %d times", len(fake.Calls))
	}
	b.workers.stop(time.Second)
}

func TestSwitchedOnCommandsStillWork(t *testing.T) {
	b, db, tg, _ := replyBot(t, 100)
	b.deps.Cfg.Features.Glossary = &config.GlossaryFeature{Enabled: true, MaxItems: 10, InPrompt: 3}
	addTerm(db, testChat, "چاییدن", "سرما خوردن", 0)
	if got := command(t, b, tg, boss, "/terms"); !strings.Contains(got, "چاییدن") {
		t.Errorf("/terms = %q", got)
	}
	if got := command(t, b, tg, boss, "/me"); got != i18n.NothingYet {
		t.Errorf("/me = %q", got)
	}
}

func TestToolsFollowTheirFeatures(t *testing.T) {
	b, _, _, _ := replyBot(t, 100)
	j := job{kind: jobReply, chatID: testChat, msg: say(b, 1, boss, "هوشنگ")}
	names := func() []string {
		tools, handlers := b.toolsFor(j)
		var out []string
		for _, tl := range tools {
			out = append(out, tl.Name)
			if handlers[tl.Name] == nil {
				t.Errorf("tool %s has no handler", tl.Name)
			}
		}
		return out
	}
	f := &b.deps.Cfg.Features
	on := names()
	for _, want := range []string{"hafez_faal", "random_poem", "react", "get_memory", "edit_memory"} {
		if !slices.Contains(on, want) {
			t.Errorf("tool %s missing with its feature on: %v", want, on)
		}
	}
	f.Faal, f.Profiles = nil, nil
	if got := names(); slices.Contains(got, "hafez_faal") || slices.Contains(got, "get_memory") || slices.Contains(got, "edit_memory") || !slices.Contains(got, "random_poem") {
		t.Errorf("faal and profiles off: %v", got)
	}
	f.Poem.Enabled = false
	if got := names(); slices.Contains(got, "random_poem") {
		t.Errorf("poem off: %v", got)
	}

	enableMedia(b)
	if got := names(); !slices.Contains(got, "find_media") || !slices.Contains(got, "send_media") {
		t.Errorf("media send on: %v", got)
	}
	f.Media.Send = false
	if got := names(); slices.Contains(got, "find_media") || slices.Contains(got, "send_media") {
		t.Errorf("media send off (describing stays on): %v", got)
	}
	if _, ok := b.mediaRefOf(&telego.Message{Sticker: &telego.Sticker{FileID: "s", FileUniqueID: "u", IsAnimated: true, Thumbnail: &telego.PhotoSize{FileID: "t"}}}); !ok {
		t.Error("media.send: false must not stop describing")
	}
}

func TestProfilesOff(t *testing.T) {
	b, db, _, fake := replyBot(t, 100, llm.Response{Content: "باشه"}, llm.Response{Content: "تعبیر خوبی داره"})
	addItems(db, testChat, ali.ID, store.KindFact, false, "عاشق فوتبال")
	addItems(db, testChat, ali.ID, store.KindOrder, true, "با علی شوخی نکن")
	b.deps.Cfg.Features.Profiles = nil

	// No profiles and no orders in the prompt, no admin rules, no memory tools.
	m := say(b, 1, boss, "هوشنگ علی رو میشناسی؟")
	b.runReply(context.Background(), job{kind: jobReply, chatID: testChat, msg: m})
	req := fake.Calls[0]
	if p := req.Messages[1].Content; strings.Contains(p, "عاشق فوتبال") || strings.Contains(p, "شوخی نکن") || strings.Contains(p, "edit_memory") {
		t.Errorf("the prompt has profile data:\n%s", p)
	}
	if strings.Contains(req.Messages[0].Content+req.Messages[1].Content, "edit_memory") {
		t.Error("the admin rules (about edit_memory) were added")
	}
	for _, name := range toolNames(req) {
		if name == "get_memory" || name == "edit_memory" {
			t.Errorf("tool %s offered", name)
		}
	}

	// No profile jobs: not after update_every messages, not by age.
	upd := memory.NewUpdater(db, fake, "m", memory.UpdaterConfig{ContextMessages: 10, MaxItems: 5, Language: "fa"}, nil)
	b.deps.Memory = upd
	for i := 10; i < 40; i++ {
		b.maybeEnqueueMemory(testChat, ali.ID)
		say(b, i, ali, "پیام")
	}
	if upd.QueueLen() != 0 {
		t.Errorf("the updater has %d jobs with profiles off", upd.QueueLen())
	}

	// The faal's تعبیر does not need a profile.
	if title := b.runFaal(context.Background(), testChat, 0, ali.ID, ""); title == "" {
		t.Error("the faal failed without profiles")
	}
	b.workers.stop(time.Second)
}

func TestGroupMemoryWorksWithoutProfiles(t *testing.T) {
	b, db, _, fake := replyBot(t, 100, llm.Response{Content: "-"})
	enableGroupMemory(b, 0)
	b.deps.Cfg.Features.Profiles = nil
	b.deps.Cfg.Bot.SpontaneousChance = 0
	b.deps.Cfg.Features.GroupMemory.UpdateEvery = 3
	upd := memory.NewUpdater(db, fake, "m", memory.UpdaterConfig{Group: memory.GroupConfig{TTL: time.Hour, MaxItems: 5}}, nil)
	b.deps.Memory = upd
	for i := 1; i <= 3; i++ {
		b.handleMessage(context.Background(), mediaMsg(i, ali, func(m *telego.Message) { m.Text = "پیام" }))
	}
	if upd.QueueLen() != 1 {
		t.Fatalf("queue = %d: only the group job is expected", upd.QueueLen())
	}
}

func TestStatusListsFeatures(t *testing.T) {
	b, _, tg, _ := replyBot(t, 100)
	b.deps.Cfg.Features.Faal = nil
	got := command(t, b, tg, boss, "/status")
	if !strings.Contains(got, "Features: profiles, poem, roast") || strings.Contains(got, "faal") {
		t.Errorf("/status = %q", got)
	}
}
