package bot

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/mymmrac/telego"

	"github.com/ehsanranjbar/hooshang/internal/faalimg"
	"github.com/ehsanranjbar/hooshang/internal/ganjoor"
	"github.com/ehsanranjbar/hooshang/internal/i18n"
	"github.com/ehsanranjbar/hooshang/internal/llm"
	"github.com/ehsanranjbar/hooshang/internal/memory"
	"github.com/ehsanranjbar/hooshang/internal/ratelimit"
	"github.com/ehsanranjbar/hooshang/internal/store"
	"github.com/ehsanranjbar/hooshang/internal/text"
)

const testChat = int64(-100)

var (
	ali  = &telego.User{ID: 7, FirstName: "علی", Username: "ali"}
	sara = &telego.User{ID: 8, FirstName: "سارا", Username: "sara"}
	boss = &telego.User{ID: ownerID, FirstName: "Ehsan", Username: "boss"}
)

// replyBot is a test bot in testChat with the owner as admin, a scripted LLM,
// a Ganjoor fixture server, and the given per-chat rate limit.
func replyBot(t *testing.T, chatLimit int, responses ...llm.Response) (*Bot, *store.DB, *fakeTG, *llm.Fake) {
	t.Helper()
	tg := newFakeTG()
	b, db := newTestBot(tg, testBotID, testBotUser)
	b.deps.Cfg.Telegram.AdminIDs = []int64{ownerID}
	b.deps.Admins = &ownerAdmins{ownerID}
	tg.member = &telego.ChatMemberOwner{Status: "creator", User: telego.User{ID: ownerID}}
	fake := &llm.Fake{Responses: responses}
	b.deps.LLM = fake
	b.deps.Limiter = ratelimit.New(1000, chatLimit, time.Now)
	b.deps.Ganjoor = ganjoor.New(ganjoorServer(t).URL, nil)
	return b, db, tg, fake
}

// ganjoorServer serves the captured Ganjoor fixtures.
func ganjoorServer(t *testing.T) *httptest.Server {
	t.Helper()
	faal, err := os.ReadFile("../ganjoor/testdata/faal.json")
	if err != nil {
		t.Fatal(err)
	}
	random, err := os.ReadFile("../ganjoor/testdata/random.json")
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/api/ganjoor/hafez/faal"):
			_, _ = w.Write(faal)
		case strings.HasPrefix(r.URL.Path, "/api/ganjoor/poem/random"):
			_, _ = w.Write(random)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// say stores a message as the bot would, and returns it.
func say(b *Bot, id int, from *telego.User, text string) *telego.Message {
	m := &telego.Message{
		MessageID: id,
		Chat:      telego.Chat{ID: testChat, Type: "supergroup", Title: "g"},
		From:      from,
		Text:      text,
		Date:      time.Now().Unix(),
	}
	b.storeIncoming(m, text)
	return m
}

func toolNames(req llm.Request) []string {
	var out []string
	for _, t := range req.Tools {
		out = append(out, t.Name)
	}
	return out
}

func TestReplyPromptHasProfilesAndTranscript(t *testing.T) {
	b, db, tg, fake := replyBot(t, 10, llm.Response{Content: "هوشنگ: سلام علی جان!"})
	addItems(db, testChat, ali.ID, store.KindFact, false, "عاشق فوتبال")
	say(b, 1, sara, "بچه‌ها امشب بازی داریم")
	m := say(b, 2, ali, "هوشنگ تو کدوم تیمی؟")

	b.runReply(context.Background(), job{kind: jobReply, chatID: testChat, msg: m})

	if len(fake.Calls) != 1 {
		t.Fatalf("LLM calls = %d", len(fake.Calls))
	}
	user := fake.Calls[0].Messages[1].Content
	for _, want := range []string{"عاشق فوتبال", "بچه‌ها امشب بازی داریم", "هوشنگ تو کدوم تیمی؟", "(@sara)"} {
		if !strings.Contains(user, want) {
			t.Errorf("prompt is missing %q:\n%s", want, user)
		}
	}
	if got := tg.sentTexts(); len(got) != 1 || got[0] != "سلام علی جان!" {
		t.Fatalf("sent = %q (the bot-name prefix must be stripped)", got)
	}
	if tg.sent[0].ReplyParameters == nil || tg.sent[0].ReplyParameters.MessageID != 2 {
		t.Fatal("reply must answer the triggering message")
	}
}

func TestAdminOnlyTools(t *testing.T) {
	for _, tc := range []struct {
		from      *telego.User
		wantAdmin bool
	}{{boss, true}, {ali, false}} {
		b, _, _, fake := replyBot(t, 10, llm.Response{Content: "باشه"})
		m := say(b, 1, tc.from, "هوشنگ درباره سارا چی میدونی؟")
		b.runReply(context.Background(), job{kind: jobReply, chatID: testChat, msg: m})

		names := strings.Join(toolNames(fake.Calls[0]), ",")
		hasAdmin := strings.Contains(names, "get_memory") || strings.Contains(names, "edit_memory")
		if hasAdmin != tc.wantAdmin {
			t.Errorf("author %d: tools = %s", tc.from.ID, names)
		}
		if !strings.Contains(names, "hafez_faal") || !strings.Contains(names, "random_poem") {
			t.Errorf("author %d: poem tools missing: %s", tc.from.ID, names)
		}
		// The admin rules end the task; the system prompt is the same for everyone (prompt cache).
		if sys := fake.Calls[0].Messages[0].Content; strings.Contains(sys, "edit_memory") {
			t.Errorf("author %d: admin rules in the system prompt", tc.from.ID)
		}
		if task := fake.Calls[0].Messages[1].Content; strings.Contains(task, "edit_memory") != tc.wantAdmin {
			t.Errorf("author %d: admin rules in the task = %v", tc.from.ID, !tc.wantAdmin)
		}
	}
}

func TestDashMeansSilence(t *testing.T) {
	b, _, tg, _ := replyBot(t, 10, llm.Response{Content: " - "})
	b.runSpontaneous(context.Background(), job{kind: jobSpontaneous, chatID: testChat, mode: "joke"})
	if n := tg.sentCount(); n != 0 {
		t.Fatalf("sent %d messages for '-'", n)
	}
}

func TestRateLimitedReplySkipsLLM(t *testing.T) {
	b, _, tg, fake := replyBot(t, 1, llm.Response{Content: "x"})
	b.deps.Limiter.Reserve(testChat, 1) // the chat's only slot is taken
	m := say(b, 1, ali, "هوشنگ؟")
	b.runReply(context.Background(), job{kind: jobReply, chatID: testChat, msg: m})
	if len(fake.Calls) != 0 || tg.sentCount() != 0 {
		t.Fatalf("calls=%d sent=%d, want 0/0", len(fake.Calls), tg.sentCount())
	}
}

func TestLongReplyRespectsLimit(t *testing.T) {
	long := strings.Repeat(strings.Repeat("ا", 99)+"\n", 90) // ~9000 runes → 3 chunks
	b, _, tg, _ := replyBot(t, 1, llm.Response{Content: long})
	m := say(b, 1, ali, "هوشنگ یه داستان بگو")
	b.runReply(context.Background(), job{kind: jobReply, chatID: testChat, msg: m})
	if n := tg.sentCount(); n != 1 {
		t.Fatalf("sent %d chunks with 1 free slot, want 1", n)
	}
}

func TestSpontaneousNeedsHalfTheBudget(t *testing.T) {
	b, _, tg, fake := replyBot(t, 4, llm.Response{Content: "x"})
	b.deps.Limiter.Reserve(testChat, 3) // 1 of 4 free < 4/2
	b.runSpontaneous(context.Background(), job{kind: jobSpontaneous, chatID: testChat, mode: "joke"})
	if len(fake.Calls) != 0 || tg.sentCount() != 0 {
		t.Fatal("spontaneous ran without headroom")
	}
}

func TestFaalCommandFlow(t *testing.T) {
	// Exactly 2 slots: the poem and the تعبیر must both fit.
	b, _, tg, fake := replyBot(t, 2, llm.Response{Content: "دلت با کسیه که خودش خبر داره 😉"})
	m := say(b, 1, ali, "/faal")
	b.handleFaal(context.Background(), m, "قبولی کنکور")

	got := tg.sentTexts()
	if len(got) != 2 {
		t.Fatalf("sent %d messages, want 2: %q", len(got), got)
	}
	if !strings.HasPrefix(got[0], i18n.FaalHeader) || !strings.Contains(got[0], "هواخواه توام جانا") ||
		!strings.Contains(got[0], i18n.MeaningLabel) || !strings.Contains(got[0], "https://ganjoor.net/hafez/ghazal/") {
		t.Errorf("message 1 = %q", got[0])
	}
	if !strings.HasPrefix(got[1], i18n.TabirPrefix) {
		t.Errorf("message 2 = %q", got[1])
	}
	if task := fake.Calls[0].Messages[1].Content; !strings.Contains(task, "قبولی کنکور") {
		t.Errorf("the wish is not in the تعبیر prompt:\n%s", task)
	}
}

func TestFaalToolFlow(t *testing.T) {
	// reply (1, pays for the card) + تعبیر (1) = 2 slots.
	b, _, tg, fake := replyBot(t, 2,
		llm.Response{ToolCalls: []llm.ToolCall{{ID: "1", Name: "hafez_faal", Arguments: `{"wish":""}`}}},
		llm.Response{Content: "تعبیرش با خودت"},     // the تعبیر call (no tools)
		llm.Response{Content: "اینم از فال حافظت!"}, // a chatty follow-up the bot must drop
	)
	m := say(b, 1, ali, "هوشنگ یه فال برام بگیر")
	b.runReply(context.Background(), job{kind: jobReply, chatID: testChat, msg: m})

	got := tg.sentTexts()
	if len(got) != 2 || !strings.HasPrefix(got[0], i18n.FaalHeader) || !strings.HasPrefix(got[1], i18n.TabirPrefix) {
		t.Fatalf("sent = %q", got)
	}
	if len(fake.Calls) != 3 || len(fake.Calls[1].Tools) != 0 {
		t.Fatalf("calls = %d (the تعبیر call must have no tools)", len(fake.Calls))
	}
}

func TestPoemWithoutPoetIsRandom(t *testing.T) {
	b, _, tg, _ := replyBot(t, 10)
	m := say(b, 1, ali, "/poem")
	b.handlePoem(context.Background(), m, "")
	got := tg.sentTexts()
	if len(got) != 1 || strings.Contains(got[0], "نمی‌شناسم") || !strings.Contains(got[0], "📜") {
		t.Fatalf("sent = %q", got)
	}
}

func TestPoemIsTwoBeytsWithThePoet(t *testing.T) {
	b, _, tg, _ := replyBot(t, 10)
	m := say(b, 1, ali, "/poem سعدی")
	b.handlePoem(context.Background(), m, "سعدی")
	got := tg.sentTexts()
	if len(got) != 1 {
		t.Fatalf("sent = %q", got)
	}
	want := "📜 آن کیست کاندر رفتنش صبر از دل ما می‌برد\nترک از خراسان آمدست از پارس یغما می‌برد\n\n" +
		"شیراز مشکین می‌کند چون ناف آهوی ختن\nگر باد نوروز از سرش بویی به صحرا می‌برد\n\n" +
		"— سعدی، غزل شمارهٔ ۱۷۶\n🔗 https://ganjoor.net/"
	if !strings.HasPrefix(got[0], want) {
		t.Fatalf("poem message:\n%s\nwant prefix:\n%s", got[0], want)
	}
}

func TestAdminEditsMemoryByTool(t *testing.T) {
	ops := `{"member":"@ali","ops":[{"action":"replace","section":"fact","old_text":"تهران","text":"ساکن شیراز"}]}`
	b, db, tg, fake := replyBot(t, 10,
		llm.Response{ToolCalls: []llm.ToolCall{{ID: "1", Name: "edit_memory", Arguments: ops}}},
		llm.Response{Content: "باشه، شیراز رو جای تهران نوشتم"},
	)
	say(b, 1, ali, "سلام")
	addItems(db, testChat, ali.ID, store.KindFact, false, "ساکن تهران")
	m := say(b, 2, boss, "هوشنگ، علی دیگه تهران نیست، رفته شیراز")
	b.runReply(context.Background(), job{kind: jobReply, chatID: testChat, msg: m})

	mem, _ := db.GetMemory(testChat, ali.ID)
	facts := mem.Kind(store.KindFact)
	if len(facts) != 1 || facts[0].Text != "ساکن شیراز" || !facts[0].Locked {
		t.Fatalf("facts = %+v", facts)
	}
	if len(mem.Suppressed) != 1 {
		t.Fatalf("suppressed = %v", mem.Suppressed)
	}
	edits, _ := db.RecentEdits(testChat, 10)
	if len(edits) != 1 || edits[0].AdminID != ownerID || edits[0].UserID != ali.ID || edits[0].After.Kind(store.KindFact)[0].Text != "ساکن شیراز" {
		t.Fatalf("edits = %+v", edits)
	}
	if res := fake.Calls[1].Messages[len(fake.Calls[1].Messages)-1]; res.Role != "tool" || !strings.Contains(res.Content, "شیراز") {
		t.Fatalf("tool result = %+v", res)
	}
	if got := tg.sentTexts(); len(got) != 1 {
		t.Fatalf("sent = %q", got)
	}
}

func TestFailedEditReportsErrorAndIsNotLogged(t *testing.T) {
	ops := `{"member":"علی","ops":[{"action":"remove","section":"facts","old_text":"اصفهان"}]}`
	b, db, _, fake := replyBot(t, 10,
		llm.Response{ToolCalls: []llm.ToolCall{{ID: "1", Name: "edit_memory", Arguments: ops}}},
		llm.Response{Content: "چنین چیزی یادم نیست"},
	)
	say(b, 1, ali, "سلام")
	m := say(b, 2, boss, "هوشنگ اصفهانی بودن علی رو فراموش کن")
	b.runReply(context.Background(), job{kind: jobReply, chatID: testChat, msg: m})

	res := fake.Calls[1].Messages[len(fake.Calls[1].Messages)-1]
	if !strings.HasPrefix(res.Content, "error:") {
		t.Fatalf("tool result = %q, want an error", res.Content)
	}
	if edits, _ := db.RecentEdits(testChat, 10); len(edits) != 0 {
		t.Fatalf("a failed edit was logged: %+v", edits)
	}
}

func TestNonAdminCannotCallEditTool(t *testing.T) {
	// Even if the model hallucinates the tool, a non-admin's job has no handler.
	ops := `{"member":"@sara","ops":[{"action":"add","section":"quirks","text":"خروپف می‌کند"}]}`
	b, db, _, _ := replyBot(t, 10,
		llm.Response{ToolCalls: []llm.ToolCall{{ID: "1", Name: "edit_memory", Arguments: ops}}},
		llm.Response{Content: "نه"},
	)
	say(b, 1, sara, "سلام")
	m := say(b, 2, ali, "هوشنگ یادت باشه سارا خروپف میکنه")
	b.runReply(context.Background(), job{kind: jobReply, chatID: testChat, msg: m})
	if q := kindTexts(db, testChat, sara.ID, store.KindQuirk); len(q) != 0 {
		t.Fatalf("a non-admin edited memory: %+v", q)
	}
}

func TestMemoryUpdateWiring(t *testing.T) {
	extracted, _ := json.Marshal(map[string]any{"items": []map[string]any{
		{"kind": "interest", "text": "فوتبال", "msgs": []int{1}}, {"kind": "fact", "text": "دانشجوی پزشکی", "msgs": []int{2}}}})
	b, db, _, fake := replyBot(t, 10, llm.Response{Content: string(extracted)})
	upd := memory.NewUpdater(db, fake, "m", memory.UpdaterConfig{ContextMessages: 150, MaxItems: 10, Language: "fa"}, nil)
	b.deps.Memory = upd
	b.deps.Cfg.Bot.SpontaneousChance = 0

	addItems(db, testChat, ali.ID, store.KindFact, true, "اهل تبریز")
	for i := 1; i <= b.deps.Cfg.Features.Profiles.UpdateEvery; i++ {
		b.handleMessage(context.Background(), &telego.Message{
			MessageID: i, Chat: telego.Chat{ID: testChat, Type: "supergroup"}, From: ali,
			Text: "پیام شماره " + string(rune('0'+i%10)), Date: time.Now().Unix(),
		})
	}
	if n := upd.QueueLen(); n != 1 {
		t.Fatalf("queue = %d after %d messages, want 1", n, b.deps.Cfg.Features.Profiles.UpdateEvery)
	}
	if err := upd.UpdateNow(context.Background(), testChat, ali.ID); err != nil {
		t.Fatal(err)
	}
	mem, _ := db.GetMember(testChat, ali.ID)
	m2, _ := db.GetMemory(testChat, ali.ID)
	r := memory.Render(m2)
	for _, want := range []string{"فوتبال", "دانشجوی پزشکی", "اهل تبریز"} {
		if !strings.Contains(r, want) {
			t.Errorf("profile is missing %q:\n%s", want, r)
		}
	}
	if mem.MsgsSinceUpdate != 0 || mem.ProfileUpdatedAt.IsZero() {
		t.Fatalf("counter=%d updatedAt=%v", mem.MsgsSinceUpdate, mem.ProfileUpdatedAt)
	}
}

func TestCommandsRunOnTheWorker(t *testing.T) {
	b, _, tg, _ := replyBot(t, 10, llm.Response{Content: "تعبیر"})
	start := time.Now()
	b.handleMessage(context.Background(), &telego.Message{
		MessageID: 1, Chat: telego.Chat{ID: testChat, Type: "supergroup"}, From: ali,
		Text: "/faal", Date: time.Now().Unix(),
	})
	if time.Since(start) > time.Second {
		t.Fatal("handleMessage blocked on the faal")
	}
	deadline := time.Now().Add(5 * time.Second)
	for tg.sentCount() < 2 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if tg.sentCount() != 2 {
		t.Fatalf("faal sent %d messages", tg.sentCount())
	}
	b.workers.stop(time.Second)
}

func TestLeakedToolMarkupIsNeverSent(t *testing.T) {
	leak := "<|tool_call>call:edit_memory{member:<|\"|>علی<|\"|>}<tool_call|>"
	b, _, tg, _ := replyBot(t, 10, llm.Response{Content: leak})
	m := say(b, 1, boss, "هوشنگ علی رفته شیراز")
	b.runReply(context.Background(), job{kind: jobReply, chatID: testChat, msg: m})
	if n := tg.sentCount(); n != 0 {
		t.Fatalf("sent %q", tg.sentTexts())
	}
	if got := cleanReply("باشه! <|tool_call>call:x{}", "هوشنگ"); got != "باشه!" {
		t.Fatalf("cleanReply = %q", got)
	}
}

// sayAt stores a message with a given age.
func sayAt(b *Bot, id int, from *telego.User, text string, age time.Duration) *telego.Message {
	m := &telego.Message{
		MessageID: id,
		Chat:      telego.Chat{ID: testChat, Type: "supergroup", Title: "g"},
		From:      from,
		Text:      text,
		Date:      time.Now().Add(-age).Unix(),
	}
	b.storeIncoming(m, text)
	return m
}

func TestOldTopicsLeaveTheTranscript(t *testing.T) {
	b, _, _, fake := replyBot(t, 10, llm.Response{Content: "x"})
	sayAt(b, 1, sara, "بچه‌ها فردا کافه همیشگی؟", 2*time.Hour)
	sayAt(b, 2, ali, "من هستم", 2*time.Hour)
	say(b, 3, sara, "کسی فیلم جدید نولان رو دیده؟")
	m := say(b, 4, ali, "هوشنگ تو دیدی؟")
	b.runReply(context.Background(), job{kind: jobReply, chatID: testChat, msg: m})

	prompt := fake.Calls[0].Messages[1].Content
	if strings.Contains(prompt, "کافه") {
		t.Errorf("a 2-hour-old topic is still in the prompt:\n%s", prompt)
	}
	if !strings.Contains(prompt, "نولان") || !strings.Contains(prompt, ">> [4] علی") {
		t.Errorf("the current exchange is missing or unmarked:\n%s", prompt)
	}
}

func TestOldMessageRepliedToIsKept(t *testing.T) {
	b, _, _, fake := replyBot(t, 10, llm.Response{Content: "x"})
	sayAt(b, 1, sara, "کی پایه‌ی کوه جمعه است؟", 3*time.Hour)
	m := say(b, 2, ali, "هوشنگ تو میای؟")
	m.ReplyToMessage = &telego.Message{MessageID: 1, From: sara}
	b.runReply(context.Background(), job{kind: jobReply, chatID: testChat, msg: m})

	prompt := fake.Calls[0].Messages[1].Content
	if !strings.Contains(prompt, "[1] سارا") || !strings.Contains(prompt, "کوه جمعه") {
		t.Errorf("the replied-to message is missing:\n%s", prompt)
	}
}

func TestProfilesFillTheBudget(t *testing.T) {
	b, db, _, fake := replyBot(t, 10, llm.Response{Content: "x"}, llm.Response{Content: "y"})
	reza := &telego.User{ID: 31, FirstName: "رضا", Username: "reza"} // never wrote in the transcript
	for _, u := range []*telego.User{ali, sara, reza} {
		_, _ = db.UpdateMember(testChat, u.ID, func(m *store.Member) { m.FirstName = u.FirstName })
		addItems(db, testChat, u.ID, store.KindQuirk, false, "عادت "+u.FirstName)
	}
	say(b, 1, sara, "سلام")
	m := say(b, 2, ali, "هوشنگ امروز هوا چطوره؟")

	// Room for everyone: the author, Sara (in the transcript) and a random member.
	b.deps.Cfg.Bot.ProfileBudget, b.deps.Cfg.Bot.RandomProfiles = 3000, 1
	b.runReply(context.Background(), job{kind: jobReply, chatID: testChat, msg: m})
	p := fake.Calls[0].Messages[1].Content
	for _, want := range []string{"عادت علی", "عادت سارا", "عادت رضا"} {
		if !strings.Contains(p, want) {
			t.Errorf("missing %q:\n%s", want, p)
		}
	}
	if strings.Index(p, "عادت علی") > strings.Index(p, "عادت سارا") {
		t.Errorf("the author should come first:\n%s", p)
	}

	// No budget: only the author, who is always shown.
	b.deps.Cfg.Bot.ProfileBudget = 0
	b.runReply(context.Background(), job{kind: jobReply, chatID: testChat, msg: m})
	p = fake.Calls[1].Messages[1].Content
	if !strings.Contains(p, "عادت علی") || strings.Contains(p, "عادت سارا") || strings.Contains(p, "عادت رضا") {
		t.Errorf("want only the author's memory:\n%s", p)
	}
}

func TestSpontaneousTargetComesFirst(t *testing.T) {
	b, db, _, fake := replyBot(t, 10, llm.Response{Content: "x"})
	b.deps.Cfg.Bot.RandomProfiles = 0
	for _, u := range []*telego.User{ali, sara} {
		_, _ = db.UpdateMember(testChat, u.ID, func(m *store.Member) { m.FirstName = u.FirstName })
		addItems(db, testChat, u.ID, store.KindQuirk, false, "عادت "+u.FirstName)
	}
	say(b, 1, sara, "سلام")
	b.runSpontaneous(context.Background(), job{kind: jobSpontaneous, chatID: testChat, mode: "tease", target: ali.ID, targetName: "علی"})
	p := fake.Calls[0].Messages[1].Content
	a, s := strings.Index(p, "عادت علی"), strings.Index(p, "عادت سارا")
	if a < 0 || s < 0 || a > s || !strings.Contains(p, "(علی first)") {
		t.Errorf("want the target first, then Sara from the transcript:\n%s", p)
	}
}

func TestFaalAsNastaliqImage(t *testing.T) {
	b, db, tg, _ := replyBot(t, 2, llm.Response{Content: "دلت روشن باشه"})
	r, err := faalimg.New("", faalimg.ShaperAuto)
	if err != nil {
		t.Fatal(err)
	}
	b.deps.FaalImage = r
	m := say(b, 1, ali, "/faal")
	b.handleFaal(context.Background(), m, "")

	if len(tg.photos) != 1 {
		t.Fatalf("photos = %d", len(tg.photos))
	}
	p := tg.photos[0]
	if !strings.HasPrefix(p.Caption, i18n.FaalHeader+" — حافظ") || !strings.Contains(p.Caption, i18n.MeaningLabel) ||
		!strings.Contains(p.Caption, "https://ganjoor.net/hafez/ghazal/") || len([]rune(p.Caption)) > 1024 {
		t.Errorf("caption (%d runes):\n%s", len([]rune(p.Caption)), p.Caption)
	}
	if p.ReplyParameters == nil || p.ReplyParameters.MessageID != 1 {
		t.Error("the faal image must reply to the request")
	}
	if got := tg.sentTexts(); len(got) != 1 || !strings.HasPrefix(got[0], i18n.TabirPrefix) {
		t.Fatalf("text messages = %q, want only the تعبیر", got)
	}
	// The image is in the transcript as a photo with its caption.
	msgs, _ := db.RecentMessages(testChat, 5)
	found := false
	for _, mm := range msgs {
		found = found || (mm.FromBot && strings.HasPrefix(mm.Text, "[عکس] "+i18n.FaalHeader))
	}
	if !found {
		t.Errorf("the image is not in the transcript: %+v", msgs)
	}
}

func TestFaalCaptionFitsTelegram(t *testing.T) {
	p := &ganjoor.Poem{FullTitle: "حافظ » غزلیات » غزل ۱", URL: "https://ganjoor.net/hafez/ghazal/sh1",
		Summary: strings.Repeat("این جمله‌ای از معنی غزل است. ", 80)}
	c := faalCaption(p)
	if n := len([]rune(c)); n > 1024 || !strings.HasSuffix(c, p.URL) || !strings.Contains(c, "است.\n\n🔗") {
		t.Fatalf("caption %d runes, ends %q", n, c[len(c)-80:])
	}
	if c := faalCaption(&ganjoor.Poem{FullTitle: "t", URL: "u"}); strings.Contains(c, i18n.MeaningLabel) {
		t.Fatalf("no summary → no معنی: %q", c)
	}
}

func TestFaalImageFallsBackToText(t *testing.T) {
	b, _, tg, _ := replyBot(t, 2, llm.Response{Content: "x"})
	b.deps.FaalImage = nil // or a failed render: the ghazal still goes out as text
	m := say(b, 1, ali, "/faal")
	b.handleFaal(context.Background(), m, "")
	if len(tg.photos) != 0 || len(tg.sentTexts()) != 2 || !strings.HasPrefix(tg.sentTexts()[0], i18n.FaalHeader) {
		t.Fatalf("photos=%d texts=%q", len(tg.photos), tg.sentTexts())
	}
}

func TestPoemAsNastaliqImage(t *testing.T) {
	b, _, tg, _ := replyBot(t, 10)
	r, err := faalimg.New("", faalimg.ShaperAuto)
	if err != nil {
		t.Fatal(err)
	}
	b.deps.PoemImage = r
	m := say(b, 1, ali, "/poem سعدی")
	b.handlePoem(context.Background(), m, "سعدی")
	if len(tg.photos) != 1 || len(tg.sentTexts()) != 0 {
		t.Fatalf("photos=%d texts=%q", len(tg.photos), tg.sentTexts())
	}
	want := "— سعدی، غزل شمارهٔ ۱۷۶\n🔗 https://ganjoor.net/saadi/divan/ghazals/sh176"
	if c := tg.photos[0].Caption; c != want {
		t.Fatalf("caption = %q, want %q", c, want)
	}
	if tg.photos[0].ReplyParameters == nil || tg.photos[0].ReplyParameters.MessageID != 1 {
		t.Fatal("the poem image must reply to the request")
	}
}

func TestTranscriptKeepsAnOldTrigger(t *testing.T) {
	b, _, _, _ := replyBot(t, 10)
	b.deps.Cfg.Bot.HistoryMessages = 3
	m := say(b, 1, ali, "هوشنگ سوال اول")
	for i := 2; i <= 6; i++ {
		say(b, i, sara, "پیام")
	}
	tr := b.renderTranscript(job{kind: jobReply, chatID: testChat, msg: m})
	if !strings.Contains(tr, ">> [1]") {
		t.Fatalf("the trigger is missing:\n%s", tr)
	}
}

func TestPoemToolFlowDropsFollowUp(t *testing.T) {
	b, _, tg, _ := replyBot(t, 10,
		llm.Response{ToolCalls: []llm.ToolCall{{ID: "1", Name: "random_poem", Arguments: `{"poet":""}`}}},
		llm.Response{Content: "بفرما، اینم شعر برای تو 😉"}, // must be dropped
	)
	m := say(b, 1, sara, "هوشنگ یه شعر بگو")
	b.runReply(context.Background(), job{kind: jobReply, chatID: testChat, msg: m})
	got := tg.sentTexts()
	if len(got) != 1 || !strings.Contains(got[0], "📜") {
		t.Fatalf("sent = %q", got)
	}
}

func TestPoemAndFaalToolsNeedARequest(t *testing.T) {
	b, _, tg, fake := replyBot(t, 10,
		llm.Response{ToolCalls: []llm.ToolCall{{ID: "1", Name: "random_poem", Arguments: `{}`}}},
		llm.Response{ToolCalls: []llm.ToolCall{{ID: "2", Name: "hafez_faal", Arguments: `{}`}}},
		llm.Response{Content: "بولت، ۹.۵۸"},
	)
	m := say(b, 1, ali, "هوشنگ رکورد دار دو ۱۰۰ متر کیه")
	b.runReply(context.Background(), job{kind: jobReply, chatID: testChat, msg: m})
	got := tg.sentTexts()
	if len(got) != 1 || got[0] != "بولت، ۹.۵۸" {
		t.Fatalf("sent = %q", got)
	}
	if res := fake.Calls[1].Messages[len(fake.Calls[1].Messages)-1].Content; !strings.Contains(res, "nobody asked") {
		t.Fatalf("tool result = %q", res)
	}
}

func TestAskedFor(t *testing.T) {
	poem := &telego.Message{From: &telego.User{ID: testBotID, IsBot: true}, Caption: "— حافظ، غزل شمارهٔ ۳۱۰"}
	for _, c := range []struct {
		text  string
		reply *telego.Message
		words []string
		want  bool
	}{
		{"هوشنگ یه شعر بخون", nil, poemWords, true},
		{"یه چیزی از سعدی", nil, append(poemWords, "سعدی"), true},
		{"نظرت درباره این آهنگ؟", nil, poemWords, false},
		{"یکی دیگه", poem, poemWords, true},
		{"یه فال بگیر", nil, faalWords, true},
		{"ریدم", nil, faalWords, false},
	} {
		m := &telego.Message{Text: c.text, ReplyToMessage: c.reply}
		if got := askedFor(job{msg: m}, c.words); got != c.want {
			t.Errorf("askedFor(%q) = %v", c.text, got)
		}
	}
}

func TestLeakedReasoningIsDropped(t *testing.T) {
	leak := "Okay, I'll provide the text and the tool call.\n\"با حافظ که کنکور می‌خوای 😂\"\n`hafez_faal(wish=\"کنکور\")`\nActually, I'll just provide the text. If I call it, it works. Wait, in these environments, I type the tool call directly."
	if got := cleanReply(leak, "هوشنگ"); got != "" {
		t.Errorf("a leaked monologue was kept: %q", got)
	}
	ok := "الان وقت Pink Floyd ـه، Wish You Were Here رو بذار و حال کن"
	if got := cleanReply(ok, "هوشنگ"); got != ok {
		t.Errorf("a Persian reply with English names was dropped: %q", got)
	}
}

func TestPoemImagesKeepTheirTextInTheTranscript(t *testing.T) {
	r, err := faalimg.New("", faalimg.ShaperGo)
	if err != nil {
		t.Fatal(err)
	}
	b, db, tg, _ := replyBot(t, 10, llm.Response{Content: "تعبیرش با خودت"})
	b.deps.FaalImage, b.deps.PoemImage = r, r

	b.handleFaal(context.Background(), say(b, 1, ali, "/faal"), "")
	b.handlePoem(context.Background(), say(b, 3, ali, "/poem"), "")
	if len(tg.photos) != 2 {
		t.Fatalf("photos = %d", len(tg.photos))
	}
	msgs, _ := db.RecentMessages(testChat, 10)
	var faal, poem string
	for _, m := range msgs {
		if !m.FromBot {
			continue
		}
		switch {
		case strings.Contains(m.Text, i18n.FaalHeader):
			faal = m.Text
		case strings.Contains(m.Text, "📜"):
			poem = m.Text
		}
	}
	// The verses (not just the caption) are stored with the images.
	if !strings.HasPrefix(faal, "[عکس] ") || !strings.Contains(faal, i18n.MeaningLabel) || strings.Count(faal, "\n") < 6 {
		t.Errorf("faal stored as %q", faal)
	}
	if !strings.HasPrefix(poem, "[عکس] 📜 ") || strings.Count(poem, "\n") < 4 {
		t.Errorf("poem stored as %q", poem)
	}
	tr := b.renderTranscript(job{kind: jobReply, chatID: testChat, msg: say(b, 9, sara, "هوشنگ این شعر یعنی چی؟")})
	if !strings.Contains(tr, firstLine(poem[len("[عکس] 📜 "):])) {
		t.Errorf("the poem is not in the transcript:\n%s", tr)
	}
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

func TestAdminOrderIsSavedAndAlwaysShown(t *testing.T) {
	ops := `{"member":"سارا","ops":[{"action":"add","section":"order","text":"باهاش شوخی نکن"}]}`
	b, db, _, fake := replyBot(t, 10,
		llm.Response{ToolCalls: []llm.ToolCall{{ID: "1", Name: "edit_memory", Arguments: ops}}},
		llm.Response{Content: "چشم"},
		llm.Response{Content: "x"},
	)
	b.deps.Cfg.Bot.ProfileBudget, b.deps.Cfg.Bot.RandomProfiles = 0, 0 // orders don't depend on profiles
	say(b, 1, sara, "سلام")
	b.runReply(context.Background(), job{kind: jobReply, chatID: testChat, msg: say(b, 2, boss, "هوشنگ دیگه با سارا شوخی نکن، جدی میگم")})
	mem, _ := db.GetMemory(testChat, sara.ID)
	if o := mem.Kind(store.KindOrder); len(o) != 1 || !o[0].Locked {
		t.Fatalf("orders = %+v", o)
	}
	// Sara wrote in the transcript; Ali asks something unrelated.
	b.runReply(context.Background(), job{kind: jobReply, chatID: testChat, msg: say(b, 3, ali, "هوشنگ چه خبر؟")})
	p := fake.Calls[2].Messages[1].Content
	if !strings.Contains(p, "standing orders") || !strings.Contains(p, "- سارا: باهاش شوخی نکن") {
		t.Errorf("order missing from the prompt:\n%s", p)
	}
}

func TestHasName(t *testing.T) {
	n := text.Fold
	for _, c := range []struct {
		text, name string
		want       bool
	}{
		{"سارا کیه؟", "سارا", true},
		{"به علی بگو", "علی", true},
		{"علیرضا اومد", "علی", false},
		{"علیو دیدی؟", "علی", true},
		{"سارا‌رو میشناسی", "سارا", true},
		{"what about ali", "ali", true},
	} {
		if got := hasName(n(c.text), n(c.name)); got != c.want {
			t.Errorf("hasName(%q, %q) = %v", c.text, c.name, got)
		}
	}
}

func TestNamedMemberProfileIsShown(t *testing.T) {
	b, db, _, fake := replyBot(t, 10, llm.Response{Content: "x"})
	b.deps.Cfg.Bot.ProfileBudget, b.deps.Cfg.Bot.RandomProfiles = 0, 0
	say(b, 1, sara, "سلام")
	addItems(db, testChat, sara.ID, store.KindFact, false, "مهندس نرم‌افزار است")
	b.runReply(context.Background(), job{kind: jobReply, chatID: testChat, msg: say(b, 2, ali, "هوشنگ سارا کیه؟")})
	if p := fake.Calls[0].Messages[1].Content; !strings.Contains(p, "مهندس نرم‌افزار") {
		t.Errorf("Sara's profile is missing:\n%s", p)
	}
}

func TestBacklogIsStoredButNotAnswered(t *testing.T) {
	b, db, tg, fake := replyBot(t, 10, llm.Response{Content: "x"})
	b.deps.Cfg.Bot.SpontaneousChance = 1
	old := &telego.Message{
		MessageID: 1, Chat: telego.Chat{ID: testChat, Type: "supergroup"}, From: ali,
		Text: "هوشنگ نظرت چیه؟", Date: time.Now().Add(-time.Hour).Unix(),
	}
	b.handleMessage(context.Background(), old)
	b.workers.stop(time.Second)
	if m, err := db.GetMessage(testChat, 1); err != nil || m.Text != "هوشنگ نظرت چیه؟" {
		t.Fatalf("backlog message not stored: %v %v", m, err)
	}
	if mem, _ := db.GetMember(testChat, ali.ID); mem == nil || mem.MsgsSinceUpdate != 1 {
		t.Fatalf("member not counted: %+v", mem)
	}
	if len(fake.Calls) != 0 || len(tg.sent) != 0 {
		t.Fatalf("the backlog was answered: %d calls, %d sent", len(fake.Calls), len(tg.sent))
	}
}

func TestVarietyNote(t *testing.T) {
	b, db, _, fake := replyBot(t, 10, llm.Response{Content: "x"})
	for i, txt := range []string{"آره بابا تو همیشه دیر میای", "ای جانم فدای اون 🥺 ت بشم", "آره بابا ولش کن"} {
		_ = db.AddMessage(testChat, int64(100+i), &store.Message{UserID: testBotID, Name: "هوشنگ", Text: txt, Date: time.Now(), FromBot: true})
	}
	m := say(b, 200, sara, "هوشنگ دلم گرفته 🥺💕")
	b.runReply(context.Background(), job{kind: jobReply, chatID: testChat, msg: m})
	p := fake.Calls[0].Messages[1].Content
	for _, want := range []string{"«آره بابا ولش»", "«آره بابا تو»", "don't use it now"} {
		if !strings.Contains(p, want) {
			t.Errorf("missing %q:\n%s", want, p)
		}
	}
	if repeatsOpening([]string{"«آره بابا تو»", "«ای جانم فدای»"}) {
		t.Error("two different openings counted as a repeat")
	}
	if note := b.varietyNote(-999); note != "" {
		t.Errorf("an empty chat got a note: %q", note)
	}
}

func TestFaalCaptionCutsOnWholeRunes(t *testing.T) {
	p := &ganjoor.Poem{FullTitle: "t", URL: "u", Summary: strings.Repeat("این یک جمله‌ی نسبتاً طولانی است؟ ", 80)}
	c := faalCaption(p)
	if strings.Contains(c, "�") || !utf8.ValidString(c) {
		t.Fatalf("caption has a broken rune: %q", c)
	}
	meaning := c[strings.Index(c, i18n.MeaningLabel)+len(i18n.MeaningLabel) : strings.Index(c, "\n\n"+i18n.PoemLink)]
	if !strings.HasSuffix(strings.TrimSpace(meaning), "؟") {
		t.Fatalf("meaning should end at a sentence: %q", meaning[len(meaning)-30:])
	}
}

func TestAPIErrorIsNotCachedAsADenial(t *testing.T) {
	b, _, tg, _ := replyBot(t, 10)
	tg.memberErr = errors.New("telegram down")
	if b.adminInGroup(context.Background(), testChat) {
		t.Fatal("allowed during an API error")
	}
	if len(tg.leaves) != 0 {
		t.Fatal("left the group on an API error")
	}
	if e := b.accessCache[testChat]; e.expiry.After(time.Now().Add(time.Minute)) {
		t.Fatalf("an API error was cached for %v", time.Until(e.expiry))
	}
}
