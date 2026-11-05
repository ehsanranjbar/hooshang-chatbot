package bot

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/mymmrac/telego"

	"github.com/ehsanranjbar/hooshang/internal/llm"
	"github.com/ehsanranjbar/hooshang/internal/memory"
	"github.com/ehsanranjbar/hooshang/internal/store"
)

func enableGroupMemory(b *Bot, chance float64) {
	g := &b.deps.Cfg.GroupMemory
	g.Enabled, g.TTL, g.MaxItems, g.UpdateEvery, g.InPrompt, g.PromptChance = true, 7*24*time.Hour, 15, 50, 4, chance
}

func TestRepliesAndReactionsAreCounted(t *testing.T) {
	b, db, _, _ := replyBot(t, 10)
	say(b, 1, sara, "فردا کوه؟")
	r := say(b, 2, ali, "من هستم")
	r.ReplyToMessage = &telego.Message{MessageID: 1}
	b.storeIncoming(r, r.Text) // stores the reply again, now as a reply to 1

	chat := telego.Chat{ID: testChat, Type: "supergroup"}
	b.handleUpdate(context.Background(), telego.Update{MessageReaction: &telego.MessageReactionUpdated{
		Chat: chat, MessageID: 1,
		NewReaction: []telego.ReactionType{&telego.ReactionTypeEmoji{Type: "emoji", Emoji: "🔥"}},
	}})
	b.handleUpdate(context.Background(), telego.Update{MessageReaction: &telego.MessageReactionUpdated{
		Chat: chat, MessageID: 1,
		NewReaction: []telego.ReactionType{&telego.ReactionTypeEmoji{Type: "emoji", Emoji: "👍"}},
	}})
	m, _ := db.GetMessage(testChat, 1)
	if m.Reactions != 2 || m.Replies != 1 || m.Engagement() != 5 {
		t.Fatalf("message 1 = %+v", m)
	}
	// The anonymous count replaces the number.
	b.handleUpdate(context.Background(), telego.Update{MessageReactionCount: &telego.MessageReactionCountUpdated{
		Chat: chat, MessageID: 1, Reactions: []telego.ReactionCount{{TotalCount: 4}, {TotalCount: 3}},
	}})
	if m, _ := db.GetMessage(testChat, 1); m.Reactions != 7 {
		t.Fatalf("reactions after the count = %d", m.Reactions)
	}
}

func TestGroupUpdateIsTriggered(t *testing.T) {
	b, db, _, fake := replyBot(t, 10, llm.Response{Content: "-"})
	enableGroupMemory(b, 0)
	b.deps.Cfg.Bot.SpontaneousChance = 0
	b.deps.Cfg.GroupMemory.UpdateEvery = 5
	upd := memory.NewUpdater(db, fake, "m", memory.UpdaterConfig{Group: memory.GroupConfig{TTL: time.Hour, MaxItems: 5}}, nil)
	b.deps.Memory = upd
	for i := 1; i <= 5; i++ {
		b.handleMessage(context.Background(), &telego.Message{
			MessageID: i, Chat: telego.Chat{ID: testChat, Type: "supergroup"}, From: []*telego.User{ali, sara}[i%2],
			Text: "پیام", Date: time.Now().Unix(),
		})
	}
	if upd.QueueLen() != 1 {
		t.Fatalf("queue = %d after 5 messages", upd.QueueLen())
	}
}

func TestGroupMemoryInPrompts(t *testing.T) {
	b, db, _, fake := replyBot(t, 10, llm.Response{Content: "x"}, llm.Response{Content: "y"}, llm.Response{Content: "z"})
	now := time.Now()
	_ = db.ReplaceGroupEvents(testChat, []store.GroupEvent{
		{Text: "برنامه کوه جمعه قطعی شد", Score: 9, FirstAt: now, LastAt: now.Add(-26 * time.Hour)},
	}, 7*24*time.Hour)
	m := say(b, 1, ali, "هوشنگ این هفته چه خبر بود؟")

	enableGroupMemory(b, 1)
	b.runReply(context.Background(), job{kind: jobReply, chatID: testChat, msg: m})
	if p := fake.Calls[0].Messages[1].Content; !strings.Contains(p, "- (دیروز) برنامه کوه جمعه قطعی شد") {
		t.Errorf("group memory missing from the reply prompt:\n%s", p)
	}
	b.runSpontaneous(context.Background(), job{kind: jobSpontaneous, chatID: testChat, mode: "comment"})
	if p := fake.Calls[1].Messages[1].Content; !strings.Contains(p, "برنامه کوه") {
		t.Errorf("group memory missing from the spontaneous prompt:\n%s", p)
	}

	enableGroupMemory(b, 0)
	other := say(b, 2, ali, "هوشنگ ناهار چی بخورم؟")
	b.runReply(context.Background(), job{kind: jobReply, chatID: testChat, msg: other})
	if p := fake.Calls[2].Messages[1].Content; strings.Contains(p, "برنامه کوه") {
		t.Errorf("prompt_chance 0 still shows the group memory:\n%s", p)
	}
	// A question about the group always gets it.
	fake.Responses = append(fake.Responses, llm.Response{Content: "w"})
	b.runReply(context.Background(), job{kind: jobReply, chatID: testChat, msg: m})
	if p := fake.Calls[3].Messages[1].Content; !strings.Contains(p, "برنامه کوه") {
		t.Errorf("a group question did not get the group memory:\n%s", p)
	}
}
