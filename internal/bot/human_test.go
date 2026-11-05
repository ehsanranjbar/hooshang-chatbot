package bot

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/mymmrac/telego"

	"github.com/ehsanranjbar/hooshang/internal/llm"
	"github.com/ehsanranjbar/hooshang/internal/store"
)

func TestSplitBubbles(t *testing.T) {
	got := splitBubbles("سلام\n---\nچطوری؟\n\n---\n\nخوبی", 3)
	if len(got) != 3 || got[0] != "سلام" || got[1] != "چطوری؟" || got[2] != "خوبی" {
		t.Fatalf("bubbles = %q", got)
	}
	if got := splitBubbles("a\n---\nb\n---\nc\n---\nd", 3); len(got) != 3 || got[2] != "c\nd" {
		t.Fatalf("over max: %q", got)
	}
	if got := splitBubbles("one line", 3); len(got) != 1 || got[0] != "one line" {
		t.Fatalf("single: %q", got)
	}
	if got := splitBubbles("a\n---\nb", 1); len(got) != 1 || got[0] != "a\nb" {
		t.Fatalf("bubbles=1 must join: %q", got)
	}
	if got := splitBubbles("---\n\n---", 3); got != nil {
		t.Fatalf("empty: %q", got)
	}
	// The model often puts the separator at the end of a sentence or inline.
	for _, in := range []string{
		"ببین صالح، احسان فقط تو تخیلاتش سازنده منه. ---\nواقعیت اینه که داره وقتشو تلف میکنه.",
		"ببین صالح، احسان فقط تو تخیلاتش سازنده منه. --- واقعیت اینه که داره وقتشو تلف میکنه.",
		"ببین صالح، احسان فقط تو تخیلاتش سازنده منه.\n--- واقعیت اینه که داره وقتشو تلف میکنه.",
		"ببین صالح، احسان فقط تو تخیلاتش سازنده منه. ----\n\nواقعیت اینه که داره وقتشو تلف میکنه.",
	} {
		got := splitBubbles(in, 3)
		if len(got) != 2 || got[0] != "ببین صالح، احسان فقط تو تخیلاتش سازنده منه." || got[1] != "واقعیت اینه که داره وقتشو تلف میکنه." {
			t.Errorf("inline separator %q: %q", in, got)
		}
	}
	// Dashes inside a word or a number range are not a separator.
	if got := splitBubbles("ساعت ۸---۹ یا pre---post؟", 3); len(got) != 1 {
		t.Errorf("dashes inside text split: %q", got)
	}
}

func TestReplyIsSentAsBubbles(t *testing.T) {
	b, _, tg, _ := replyBot(t, 10, llm.Response{Content: "سلام علی\n---\nچه خبر؟"})
	m := say(b, 1, ali, "هوشنگ")
	b.runReply(context.Background(), job{kind: jobReply, chatID: testChat, msg: m})
	texts := tg.sentTexts()
	if len(texts) != 2 || texts[0] != "سلام علی" || texts[1] != "چه خبر؟" {
		t.Fatalf("sent %q", texts)
	}
	if tg.sent[0].ReplyParameters == nil || tg.sent[1].ReplyParameters != nil {
		t.Error("only the first bubble should reply to the trigger")
	}
	// The reply ended with a question to Ali: his next message counts as the answer.
	c, _ := b.deps.DB.GetChat(testChat)
	if c.LastBotQuestion.UserID != ali.ID {
		t.Fatalf("question not remembered: %+v", c.LastBotQuestion)
	}
	next := say(b, 2, ali, "هیچی، خسته‌ام")
	if !b.isFollowUp(c, next) {
		t.Error("Ali's next message is not a follow-up")
	}
	c, _ = b.deps.DB.GetChat(testChat)
	if c.LastBotQuestion.UserID != 0 {
		t.Error("the question should be forgotten once answered")
	}
	if b.isFollowUp(store.Chat{LastBotQuestion: store.BotQuestion{UserID: sara.ID, At: time.Now()}}, next) {
		t.Error("someone else's message counted as the answer")
	}
	if b.isFollowUp(store.Chat{LastBotQuestion: store.BotQuestion{UserID: ali.ID, At: time.Now().Add(-5 * time.Minute)}}, next) {
		t.Error("an old question still counted")
	}
}

func TestBubblesJoinWhenRateLimited(t *testing.T) {
	b, _, tg, _ := replyBot(t, 1, llm.Response{Content: "یک\n---\nدو\n---\nسه"})
	m := say(b, 1, ali, "هوشنگ")
	b.runReply(context.Background(), job{kind: jobReply, chatID: testChat, msg: m})
	if texts := tg.sentTexts(); len(texts) != 1 || texts[0] != "یک\nدو\nسه" {
		t.Fatalf("sent %q, want one joined message", texts)
	}
}

func TestMentionsBecomeEntities(t *testing.T) {
	b, db := newTestBot(newFakeTG(), testBotID, testBotUser)
	_, _ = db.UpdateMember(testChat, 42, func(m *store.Member) { m.FirstName = "سارا" })
	text, ents := b.mentions(testChat, "🙂 {mention:42} بیا اینجا {mention:999}")
	if text != "🙂 سارا بیا اینجا" {
		t.Fatalf("text = %q", text)
	}
	if len(ents) != 1 || ents[0].Type != telego.EntityTypeTextMention || ents[0].User.ID != 42 {
		t.Fatalf("entities = %+v", ents)
	}
	if ents[0].Offset != 3 || ents[0].Length != 4 { // the emoji is two UTF-16 units, plus the space
		t.Errorf("offset/length = %d/%d, want 3/4", ents[0].Offset, ents[0].Length)
	}
	plain, none := b.mentions(testChat, "no markers")
	if plain != "no markers" || none != nil {
		t.Error("plain text changed")
	}
}

func TestQuietHours(t *testing.T) {
	at := func(h, m int) time.Time { return time.Date(2026, 11, 5, h, m, 0, 0, time.UTC) }
	if !quiet(at(2, 0), 90, 480) || quiet(at(9, 0), 90, 480) || !quiet(at(1, 30), 90, 480) || quiet(at(8, 0), 90, 480) {
		t.Error("range crossing midnight")
	}
	if !quiet(at(13, 0), 12*60, 14*60) || quiet(at(15, 0), 12*60, 14*60) {
		t.Error("same-day range")
	}
	b, _ := newTestBot(newFakeTG(), testBotID, testBotUser)
	b.deps.Cfg.Bot.Human.QuietHours = ""
	if b.inQuietHours(time.Now()) {
		t.Error("no quiet hours configured")
	}
}

func TestMoodOfDayIsStable(t *testing.T) {
	b, _ := newTestBot(newFakeTG(), testBotID, testBotUser)
	now := time.Now()
	a, c := b.moodOfDay(testChat, now), b.moodOfDay(testChat, now.Add(time.Hour))
	if a != c && now.In(tehran).Day() == now.Add(time.Hour).In(tehran).Day() {
		t.Errorf("mood changed within the day: %s vs %s", a, c)
	}
	b.deps.Cfg.Bot.Human.Moods = []string{"grumpy"}
	if b.moodOfDay(testChat, now) != "grumpy" {
		t.Error("configured moods ignored")
	}
	if got := b.systemPrompt(testChat); !strings.Contains(got, "Today you are a bit grumpy") {
		t.Errorf("mood missing from the system prompt:\n%s", got)
	}
	if temp := b.moodTemperature(testChat); temp >= b.deps.Cfg.LLM.Temperature {
		t.Errorf("grumpy should lower the temperature: %v", temp)
	}
}

func TestTypingDelay(t *testing.T) {
	b, _ := newTestBot(newFakeTG(), testBotID, testBotUser)
	h := &b.deps.Cfg.Bot.Human
	h.Typing.Enabled, h.Typing.CharsPerSecond, h.Typing.Min, h.Typing.Max = true, 25, time.Second, 6*time.Second
	if d := b.typingDelay("سلام"); d != time.Second {
		t.Errorf("short text: %v, want the minimum", d)
	}
	if d := b.typingDelay(strings.Repeat("ا", 1000)); d != 6*time.Second {
		t.Errorf("long text: %v, want the maximum", d)
	}
	if d := b.typingDelay(strings.Repeat("ا", 75)); d != 3*time.Second {
		t.Errorf("75 chars at 25/s: %v, want 3s", d)
	}
	h.Typing.Enabled = false
	if b.typingDelay("x") != 0 {
		t.Error("disabled typing still delays")
	}
}
