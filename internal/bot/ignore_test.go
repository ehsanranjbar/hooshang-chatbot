package bot

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/mymmrac/telego"

	"github.com/ehsanranjbar/hooshang/internal/i18n"
	"github.com/ehsanranjbar/hooshang/internal/llm"
	"github.com/ehsanranjbar/hooshang/internal/store"
)

// groupFrom builds a fresh testChat message.
func groupFrom(id int, from *telego.User, text string) *telego.Message {
	return &telego.Message{
		MessageID: id, Chat: telego.Chat{ID: testChat, Type: "supergroup", Title: "g"},
		From: from, Text: text, Date: time.Now().Unix(),
	}
}

func TestIgnoreMePurgesAndConfirms(t *testing.T) {
	b, db, tg, _ := replyBot(t, 100, llm.Response{Content: "x"})
	ctx := context.Background()
	b.deps.Cfg.Bot.SpontaneousChance = 0
	b.handleMessage(ctx, groupFrom(1, ali, "یه چیز خصوصی"))
	b.handleMessage(ctx, groupFrom(2, sara, "سلام"))
	_, _ = db.UpdateMember(testChat, ali.ID, func(m *store.Member) {
		m.Username, m.NoTease = "ali", true
		m.Profile.Facts = []store.Item{{Text: "عاشق فوتبال"}}
		m.Profile.Orders = []store.Item{{Text: "صداش کن مهندس", Locked: true}}
	})

	b.handleMessage(ctx, groupFrom(3, ali, "/ignoreme"))

	if got := tg.sentTexts(); len(got) != 1 || got[0] != i18n.IgnoreOn {
		t.Fatalf("sent = %q", got)
	}
	msgs, _ := db.RecentMessages(testChat, 50)
	for _, m := range msgs {
		if m.UserID == ali.ID {
			t.Errorf("a message of the ignored member survived: %+v", m)
		}
	}
	mem, err := db.GetMember(testChat, ali.ID)
	if err != nil {
		t.Fatal(err)
	}
	want := store.Member{ID: mem.ID, FirstName: "علی", Ignored: true}
	if mem.FirstName != want.FirstName || !mem.Ignored || mem.Username != "" || mem.NoTease || mem.LastName != "" ||
		len(mem.Profile.Facts) != 0 || len(mem.Profile.Orders) != 0 || !mem.LastSeen.IsZero() || mem.MsgsSinceUpdate != 0 {
		t.Fatalf("member = %+v, want a minimal ignored record", mem)
	}
	if _, err := db.MemberByUsername(testChat, "ali"); err == nil {
		t.Error("the username of an ignored member is still indexed")
	}
	if m, _ := db.GetMessage(testChat, 2); m == nil || m.UserID != sara.ID {
		t.Error("someone else's message was purged")
	}
}

func TestIgnoreMeWorksWithProfilesOff(t *testing.T) {
	b, db, tg, _ := replyBot(t, 100, llm.Response{Content: "x"})
	b.deps.Cfg.Features.Profiles = nil
	b.handleMessage(context.Background(), groupFrom(1, ali, "/ignoreme"))
	if got := tg.sentTexts(); len(got) != 1 || got[0] != i18n.IgnoreOn {
		t.Fatalf("sent = %q", got)
	}
	if !isIgnoredIn(db, ali.ID) {
		t.Fatal("not ignored")
	}
}

func isIgnoredIn(db *store.DB, id int64) bool {
	m, err := db.GetMember(testChat, id)
	return err == nil && m.Ignored
}

func TestIgnoredMemberIsInvisible(t *testing.T) {
	b, db, tg, fake := replyBot(t, 100, llm.Response{Content: "x"})
	ctx := context.Background()
	b.deps.Cfg.Bot.ReactionChance = 1
	b.deps.Cfg.Bot.SpontaneousChance = 1
	b.handleMessage(ctx, groupFrom(1, ali, "/ignoreme"))
	chatBefore, _ := db.GetChat(testChat)
	memBefore, _ := db.GetMember(testChat, ali.ID)
	tg.mu.Lock()
	tg.sent = nil
	tg.mu.Unlock()

	named := groupFrom(5, ali, "هوشنگ سلام، چطوری؟")
	if !b.addressed(named, "") {
		t.Fatal("the test message does not name the bot")
	}
	for i, m := range []*telego.Message{
		named,
		groupFrom(6, ali, "یه پیام معمولی"),
		groupFrom(7, ali, "/roast"),
		groupFrom(8, ali, "/me"),
		groupFrom(9, ali, "/notease"),
		{MessageID: 10, Chat: named.Chat, From: ali, Date: time.Now().Unix(), Voice: &telego.Voice{FileID: "v", Duration: 3}},
	} {
		b.handleMessage(ctx, m)
		if _, err := db.GetMessage(testChat, int64(m.MessageID)); err == nil {
			t.Errorf("message %d was stored", i)
		}
	}
	if b.workers.Len() != 0 || len(fake.Calls) != 0 || tg.sentCount() != 0 || len(tg.reactions) != 0 {
		t.Fatalf("the bot reacted to an ignored member: workers %d, llm %d, sent %d, reactions %d",
			b.workers.Len(), len(fake.Calls), tg.sentCount(), len(tg.reactions))
	}
	chatAfter, _ := db.GetChat(testChat)
	memAfter, _ := db.GetMember(testChat, ali.ID)
	if chatAfter.MsgsSinceBot != chatBefore.MsgsSinceBot || memAfter.MsgsSinceUpdate != memBefore.MsgsSinceUpdate ||
		memAfter.NoTease || !memAfter.LastSeen.IsZero() {
		t.Fatalf("an ignored message was counted: chat %+v member %+v", chatAfter, memAfter)
	}
	// Control: the same message from someone who is not ignored does start a reply.
	b.handleMessage(ctx, groupFrom(11, sara, "هوشنگ سلام، چطوری؟"))
	for deadline := time.Now().Add(5 * time.Second); tg.sentCount() == 0 && time.Now().Before(deadline); {
		time.Sleep(5 * time.Millisecond)
	}
	b.workers.stop(5 * time.Second)
	if len(fake.Calls) == 0 {
		t.Fatal("control: a message naming the bot did not reach the LLM")
	}
}

func TestUnignoreMeRestores(t *testing.T) {
	b, db, tg, _ := replyBot(t, 100, llm.Response{Content: "x"})
	ctx := context.Background()
	b.deps.Cfg.Bot.SpontaneousChance = 0
	b.handleMessage(ctx, groupFrom(1, ali, "/ignoreme"))
	b.handleMessage(ctx, groupFrom(2, ali, "پنهان"))
	if _, err := db.GetMessage(testChat, 2); err == nil {
		t.Fatal("stored while ignored")
	}

	b.handleMessage(ctx, groupFrom(3, ali, "/unignoreme@"+testBotUser))
	if got := tg.sentTexts(); got[len(got)-1] != i18n.IgnoreOff {
		t.Fatalf("sent = %q", got)
	}
	if isIgnoredIn(db, ali.ID) {
		t.Fatal("still ignored")
	}
	if _, err := db.GetMessage(testChat, 3); err == nil {
		t.Error("the /unignoreme command itself was stored")
	}
	b.handleMessage(ctx, groupFrom(4, ali, "برگشتم"))
	if m, err := db.GetMessage(testChat, 4); err != nil || m.Text != "برگشتم" {
		t.Fatalf("a message after /unignoreme was not stored: %v", err)
	}
	if mem, _ := db.GetMember(testChat, ali.ID); mem.Username != "ali" || mem.LastName != "" || mem.MsgsSinceUpdate != 1 {
		t.Errorf("member after return = %+v", mem)
	}

	// A stale /unignoreme (backlog after downtime) does not run.
	b.handleMessage(ctx, groupFrom(5, ali, "/ignoreme"))
	old := groupFrom(6, ali, "/unignoreme")
	old.Date = time.Now().Add(-time.Hour).Unix()
	b.handleMessage(ctx, old)
	if !isIgnoredIn(db, ali.ID) {
		t.Error("a stale /unignoreme was executed")
	}
}

func TestIgnoredMembersNeverAppear(t *testing.T) {
	b, db, tg, _ := replyBot(t, 100, llm.Response{Content: "x"})
	b.deps.Cfg.Bot.RandomProfiles = 5
	for _, u := range []*telego.User{ali, sara} {
		say(b, int(u.ID), u, "سلام من "+u.FirstName)
		_, _ = db.UpdateMember(testChat, u.ID, func(m *store.Member) {
			m.Profile.Facts = []store.Item{{Text: "راز-" + u.FirstName}}
			m.Profile.Orders = []store.Item{{Text: "دستور-" + u.FirstName, Locked: true}}
		})
	}
	// Ali is ignored (the flag is what counts; his record is rich only to prove the filter).
	_, _ = db.UpdateMember(testChat, ali.ID, func(m *store.Member) { m.Ignored = true })

	trigger := say(b, 50, sara, "هوشنگ، علی رو @ali می‌شناسی؟")
	trigger.Entities = []telego.MessageEntity{{Type: telego.EntityTypeMention, Offset: 17, Length: 4}}
	trigger.ReplyToMessage = &telego.Message{MessageID: 7, From: ali}
	j := job{kind: jobReply, chatID: testChat, msg: trigger, target: ali.ID}
	msgs := []store.Message{{ID: 7, UserID: ali.ID, Name: "علی", Text: "x"}, {ID: 50, UserID: sara.ID, Name: "سارا", Text: "y"}}

	profiles := b.renderProfiles(j, msgs)
	orders := b.renderOrders(j, msgs)
	for name, text := range map[string]string{"profiles": profiles, "orders": orders} {
		if strings.Contains(text, "علی") || strings.Contains(text, "راز-علی") || strings.Contains(text, "دستور-علی") {
			t.Errorf("an ignored member is in the %s:\n%s", name, text)
		}
	}
	if !strings.Contains(profiles, "راز-سارا") || !strings.Contains(orders, "دستور-سارا") {
		t.Errorf("the control member is missing:\n%s\n%s", profiles, orders)
	}
	if ids := b.aboutIDs(j); len(ids) != 0 {
		t.Errorf("aboutIDs = %v", ids)
	}
	if ids := b.namedIDs(testChat, "علی و سارا"); len(ids) != 1 || ids[0] != sara.ID {
		t.Errorf("namedIDs = %v", ids)
	}

	// Never a spontaneous target.
	members, _ := db.Members(testChat)
	mmap := map[int64]*store.Member{}
	for _, m := range members {
		mmap[m.UserID()] = m
	}
	for roll := 0.0; roll < 1; roll += 0.1 {
		id, ok := PickTarget([]store.Message{{UserID: ali.ID}, {UserID: sara.ID}}, mmap, testBotID, roll)
		if !ok || id != sara.ID {
			t.Fatalf("PickTarget(%v) = %d, %v", roll, id, ok)
		}
	}
	if _, ok := PickTarget([]store.Message{{UserID: ali.ID}}, mmap, testBotID, 0.5); ok {
		t.Error("an ignored author is a target")
	}
	say(b, 60, ali, "")
	if id, ok := b.pickTarget(testChat); ok && id == ali.ID {
		t.Error("pickTarget returned an ignored member")
	}

	// /roast refuses.
	roast := groupFrom(70, sara, "/roast")
	roast.ReplyToMessage = &telego.Message{MessageID: 7, From: ali}
	b.handleCommand(context.Background(), roast, roast.Text)
	if got := tg.sentTexts(); len(got) != 1 || got[0] != i18n.IgnoredTarget || b.workers.Len() != 0 {
		t.Fatalf("roast of an ignored member: sent %q, workers %d", got, b.workers.Len())
	}

	// The memory tools cannot reach them either.
	if _, err := b.resolveMember(testChat, "@ali", j); err == nil {
		t.Error("resolveMember found an ignored member by username")
	}
	if _, err := b.resolveMember(testChat, "", job{msg: trigger}); err == nil {
		t.Error("resolveMember found an ignored member by reply")
	}
	if id, err := b.resolveMember(testChat, "سارا", j); err != nil || id != sara.ID {
		t.Errorf("control: %d %v", id, err)
	}
}
