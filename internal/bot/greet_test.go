package bot

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/mymmrac/telego"

	"github.com/ehsanranjbar/hooshang/internal/config"
	"github.com/ehsanranjbar/hooshang/internal/llm"
)

func enableGreetings(b *Bot) {
	b.deps.Cfg.Features.Greetings = &config.GreetingsFeature{Enabled: true, Chance: 1, MemberCooldown: 12 * time.Hour, ChatCooldown: 30 * time.Minute, Welcome: true, Farewell: true}
	b.deps.Cfg.Bot.Human.QuietHours = ""
}

func TestGreetingKind(t *testing.T) {
	cases := map[string]string{
		"صبح بخیر همگی":        "greet_morning",
		"صبحتون بخیر ☀️":       "greet_morning",
		"Good morning guys":    "greet_morning",
		"شب بخیر، من رفتم":     "greet_night",
		"خب بچه‌ها شبتون بخیر": "greet_night",
		"gn":                   "greet_night",
		"امروز صبح رفتم بانک":  "",
		"سلام":                 "",
	}
	for in, want := range cases {
		if got := greetingKind(in); got != want {
			t.Errorf("greetingKind(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestGreetingsAreAnsweredWithCooldowns(t *testing.T) {
	b, db, _, _ := replyBot(t, 10)
	enableGreetings(b)
	b.workers.stop(time.Second) // keep the jobs from running: we only check the decision
	m := say(b, 1, ali, "صبح بخیر")
	c, _ := db.GetChat(testChat)
	if !b.maybeGreet(c, m, m.Text) {
		t.Fatal("a greeting with chance 1 was not answered")
	}
	c, _ = db.GetChat(testChat)
	mem, _ := db.GetMember(testChat, ali.ID)
	if c.LastGreetingAt.IsZero() || mem.LastGreetedAt.IsZero() {
		t.Fatal("cooldowns not recorded")
	}
	if b.maybeGreet(c, say(b, 2, sara, "صبح بخیر"), "صبح بخیر") {
		t.Error("the chat cooldown was ignored")
	}
	c.LastGreetingAt = time.Now().Add(-time.Hour)
	if !b.maybeGreet(c, say(b, 3, sara, "صبح بخیر"), "صبح بخیر") {
		t.Error("another member after the chat cooldown should be greeted")
	}
	c.LastGreetingAt = time.Now().Add(-time.Hour)
	if b.maybeGreet(c, say(b, 4, ali, "شب بخیر"), "شب بخیر") {
		t.Error("the member cooldown was ignored")
	}
	if b.maybeGreet(c, say(b, 5, sara, "چه خبر"), "چه خبر") {
		t.Error("a plain message counted as a greeting")
	}
	b.deps.Cfg.Features.Greetings = nil
	if b.maybeGreet(c, say(b, 6, sara, "صبح بخیر"), "صبح بخیر") {
		t.Error("greetings off still greeted")
	}
}

func TestWelcomeAndFarewell(t *testing.T) {
	b, db, tg, fake := replyBot(t, 10, llm.Response{Content: "خوش اومدی رضا، اینجا هیچ‌کس جدی نیست"}, llm.Response{Content: "رضا رفت، یه نفر کمتر که غر بزنه"})
	enableGreetings(b)
	reza := telego.User{ID: 777, FirstName: "رضا"}
	join := &telego.Message{MessageID: 1, Chat: telego.Chat{ID: testChat, Type: "supergroup"}, From: &reza, Date: time.Now().Unix(), NewChatMembers: []telego.User{reza, {ID: 5, FirstName: "bot", IsBot: true}}}
	b.handleMessage(context.Background(), join)
	waitSent(t, tg, 1)
	if mem, err := db.GetMember(testChat, reza.ID); err != nil || mem.FirstName != "رضا" {
		t.Fatalf("new member not stored: %v %v", mem, err)
	}
	if p := fake.Calls[0].Messages[1].Content; !strings.Contains(p, "just joined") || !strings.Contains(p, "رضا") {
		t.Errorf("welcome task:\n%s", p)
	}
	leave := &telego.Message{MessageID: 2, Chat: telego.Chat{ID: testChat, Type: "supergroup"}, From: &reza, Date: time.Now().Unix(), LeftChatMember: &reza}
	b.handleMessage(context.Background(), leave)
	waitSent(t, tg, 2)
	if p := fake.Calls[1].Messages[1].Content; !strings.Contains(p, "just left") {
		t.Errorf("farewell task:\n%s", p)
	}
	b.workers.stop(time.Second)
	if n, _ := db.RecentMessages(testChat, 10); len(n) != 2 { // only the bot's two lines are stored
		t.Errorf("stored %d messages, want the bot's 2", len(n))
	}
}

func TestQuietHoursSilenceSpontaneity(t *testing.T) {
	b, _, _, _ := replyBot(t, 10, llm.Response{Content: "-"}, llm.Response{Content: "-"}, llm.Response{Content: "-"})
	enableGreetings(b)
	b.deps.Cfg.Bot.Human.QuietHours = "00:00-23:59"
	b.deps.Cfg.Bot.ReactionChance = 1
	b.rollFn = func() float64 { return 0 }
	m := say(b, 1, ali, "صبح بخیر")
	b.replyOrSpontaneous(m, "")
	c, _ := b.deps.DB.GetChat(testChat)
	if !c.LastGreetingAt.IsZero() || b.workers.Len() != 0 {
		t.Error("quiet hours did not silence the bot")
	}
	b.deps.Cfg.Bot.Human.QuietHours = ""
	b.replyOrSpontaneous(say(b, 3, sara, "صبح بخیر"), "")
	c, _ = b.deps.DB.GetChat(testChat)
	if c.LastGreetingAt.IsZero() {
		t.Error("outside quiet hours the greeting was not answered")
	}
	b.deps.Cfg.Bot.Human.QuietHours = "00:00-23:59"
	// Addressed messages are still answered in quiet hours.
	b.replyOrSpontaneous(say(b, 2, ali, "هوشنگ خوابی؟"), "")
	if b.workers.Len() != 1 {
		t.Error("an addressed message was not queued during quiet hours")
	}
	b.workers.stop(time.Second)
}

// waitSent waits until the fake has sent n messages (the worker runs them).
func waitSent(t *testing.T, tg *fakeTG, n int) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for tg.sentCount() < n {
		if time.Now().After(deadline) {
			t.Fatalf("sent %d messages, want %d", tg.sentCount(), n)
		}
		time.Sleep(5 * time.Millisecond)
	}
}
