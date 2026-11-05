package bot

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/ehsanranjbar/hooshang/internal/llm"
	"github.com/ehsanranjbar/hooshang/internal/store"
)

func TestReactionEmoji(t *testing.T) {
	for in, want := range map[string]string{
		"😂": "", "🤣": "🤣", "❤️": "❤", " 👍 ": "👍", "اینو میدم 🔥": "🔥", "❤️‍🔥": "❤‍🔥", "-": "",
	} {
		got, ok := reactionEmoji(in)
		if got != want || ok != (want != "") {
			t.Errorf("reactionEmoji(%q) = %q, %v; want %q", in, got, ok, want)
		}
	}
}

func TestReactOnlyReply(t *testing.T) {
	b, db, tg, _ := replyBot(t, 10,
		llm.Response{ToolCalls: []llm.ToolCall{{ID: "1", Name: "react", Arguments: `{"emoji":"🤣"}`}}},
		llm.Response{Content: "-"},
	)
	m := say(b, 1, ali, "هوشنگ مرسی بابت دیروز 😂")
	b.runReply(context.Background(), job{kind: jobReply, chatID: testChat, msg: m})
	if got := tg.reacted(); len(got) != 1 || got[0] != "🤣" || tg.reactions[0].MessageID != 1 {
		t.Fatalf("reactions = %v", got)
	}
	if len(tg.sent) != 0 {
		t.Fatalf("a react-only answer sent %q", tg.sentTexts())
	}
	if s, _ := db.GetMessage(testChat, 1); s.BotReaction != "🤣" {
		t.Fatalf("reaction not stored: %+v", s)
	}
	tr := b.renderTranscript(job{kind: jobReply, chatID: testChat, msg: say(b, 2, sara, "هوشنگ؟")})
	if !strings.Contains(tr, "مرسی بابت دیروز 😂 {you reacted 🤣}") {
		t.Errorf("the transcript doesn't show the reaction:\n%s", tr)
	}
}

func TestSpontaneousReaction(t *testing.T) {
	b, db, tg, fake := replyBot(t, 10, llm.Response{Content: "🔥"})
	m := say(b, 1, sara, "بالاخره کارمو گرفتم!")
	b.runReaction(context.Background(), job{kind: jobReaction, chatID: testChat, msg: m})
	if got := tg.reacted(); len(got) != 1 || got[0] != "🔥" {
		t.Fatalf("reactions = %v", got)
	}
	if p := fake.Calls[0].Messages[1].Content; !strings.Contains(p, ">> [1]") || !strings.Contains(p, "exactly -") {
		t.Errorf("reaction prompt:\n%s", p)
	}
	if c, _ := db.GetChat(testChat); c.LastReactionAt.IsZero() {
		t.Error("cooldown not started")
	}
	if len(tg.sent) != 0 || len(tg.actions) != 0 {
		t.Errorf("a reaction sent %d messages / %d typing actions", len(tg.sent), len(tg.actions))
	}
}

func TestShouldReact(t *testing.T) {
	now := time.Now()
	if !ShouldReact(store.Chat{}, now, 0.2, time.Minute, 0.1) {
		t.Error("should react under the chance")
	}
	if ShouldReact(store.Chat{}, now, 0.2, time.Minute, 0.3) {
		t.Error("reacted over the chance")
	}
	if ShouldReact(store.Chat{LastReactionAt: now.Add(-30 * time.Second)}, now, 1, time.Minute, 0) {
		t.Error("reacted inside the cooldown")
	}
	if ShouldReact(store.Chat{Muted: true}, now, 1, 0, 0) {
		t.Error("reacted while muted")
	}
}

func TestWrittenReactionMarkerBecomesAReaction(t *testing.T) {
	for content, want := range map[string]string{
		"{you reacted 😂}\n-":                         "",
		"{you reacted 😂}\nسارا، این استیکر":          "سارا، این استیکر",
		"{ \"emoji\": \"😂\" }\nبه این راحتی‌ها نیست": "به این راحتی‌ها نیست",
	} {
		b, db, tg, _ := replyBot(t, 10, llm.Response{Content: content})
		m := say(b, 1, ali, "هوشنگ دقیقا همین بود")
		b.runReply(context.Background(), job{kind: jobReply, chatID: testChat, msg: m})
		if got := tg.reacted(); len(got) != 1 || got[0] != "🤣" {
			t.Errorf("%q: reactions = %v", content, got)
		}
		sent := strings.Join(tg.sentTexts(), "|")
		if sent != want || strings.Contains(sent, "reacted") {
			t.Errorf("%q: sent %q, want %q", content, sent, want)
		}
		if s, _ := db.GetMessage(testChat, 1); s.BotReaction != "🤣" {
			t.Errorf("%q: stored reaction %q", content, s.BotReaction)
		}
	}
}

func TestToolArgumentsAsTextAreNotSent(t *testing.T) {
	rest, emojis := splitReactionMarkers("{\"query\": \"خنده\", \"kind\": \"sticker\"}\nبفرما")
	if strings.TrimSpace(rest) != "بفرما" || len(emojis) != 0 {
		t.Fatalf("rest %q emojis %v", rest, emojis)
	}
	if rest, _ := splitReactionMarkers("قیمتش {۲۰} تومنه"); rest != "قیمتش {۲۰} تومنه" {
		t.Fatalf("plain braces were touched: %q", rest)
	}
}

func TestSplitReactionMarkers(t *testing.T) {
	rest, emojis := splitReactionMarkers("(reacted 🔥) سلام {you reacted ❤️}")
	if strings.TrimSpace(rest) != "سلام" || len(emojis) != 2 || emojis[0] != "🔥" || emojis[1] != "❤" {
		t.Fatalf("rest %q emojis %v", rest, emojis)
	}
}
