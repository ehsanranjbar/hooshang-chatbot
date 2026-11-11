package bot

import (
	"context"
	"github.com/mymmrac/telego"
	"strings"
	"testing"
	"time"

	"github.com/ehsanranjbar/hooshang/internal/config"
	"github.com/ehsanranjbar/hooshang/internal/llm"
	"github.com/ehsanranjbar/hooshang/internal/store"
)

// enableRAG turns retrieval on with the fake embedder and runs its queue
// synchronously through flush.
func enableRAG(b *Bot) {
	b.deps.Cfg.LLM.EmbeddingModel = "fake-embed"
	b.deps.Cfg.Features.RAG = &config.RAGFeature{Enabled: true, ArchiveTTL: 24 * time.Hour, TopK: 3, MinScore: 0.2, DecayDays: 30, Batch: 16, MaxQueue: 100}
	b.embedder = newEmbedder(b)
}

// flushAll embeds everything pending.
func flushAll(b *Bot) {
	for b.embedder.flush(context.Background()) {
	}
}

func TestArchiveAndRelatedMessages(t *testing.T) {
	b, db, _, fake := replyBot(t, 10, llm.Response{Content: "x"})
	enableRAG(b)
	b.deps.Cfg.Bot.HistoryMessages = 3
	// An old conversation about a car, then unrelated chatter that fills the transcript window.
	say(b, 1, ali, "ماشینمو فروختم، پراید سفید بود و خیلی دوستش داشتم")
	say(b, 2, sara, "چه حیف، همون پراید سفید که باهاش شمال رفتیم؟")
	for i := 3; i <= 8; i++ {
		say(b, i, sara, "امروز هوا خیلی خوبه بچه‌ها بریم بیرون")
	}
	flushAll(b)
	if a, err := db.GetArchived(testChat, 1); err != nil || a.UserID != ali.ID {
		t.Fatalf("message not archived: %v %v", a, err)
	}
	if st, err := db.MsgIndexStats(); err != nil || st.Unplaced != 8 {
		t.Fatalf("index has %+v (%v), want 8 vectors", st, err)
	}
	m := say(b, 9, ali, "هوشنگ یادته ماشینمو فروختم؟ پراید سفید")
	flushAll(b)
	b.runReply(context.Background(), job{kind: jobReply, chatID: testChat, msg: m})
	p := fake.Calls[0].Messages[1].Content
	if !strings.Contains(p, "Older messages that may be relevant") || !strings.Contains(p, "پراید سفید بود") {
		t.Errorf("related messages missing:\n%s", p)
	}
	if strings.Count(p, "ماشینمو فروختم، پراید سفید بود") != 1 {
		t.Errorf("the archived line should appear once:\n%s", p)
	}
	// A short trigger or a greeting is not worth a retrieval, unless it
	// replies to a message that says enough.
	for _, c := range []struct {
		text, reply string
		want        bool
	}{
		{"هوشنگ یادته ماشینمو فروختم؟ پراید سفید", "", true},
		{"هوشنگ؟", "", false},
		{"شب بخیر هوشنگ جان، خوب بخوابی", "", false},
		{"هوشنگ؟", "ماشینمو فروختم، پراید سفید بود و خیلی دوستش داشتم", true},
	} {
		tm := &telego.Message{Text: c.text}
		if c.reply != "" {
			tm.ReplyToMessage = &telego.Message{Text: c.reply}
		}
		if got := worthRetrieving(&job{msg: tm}); got != c.want {
			t.Errorf("worthRetrieving(%q, reply %q) = %v", c.text, c.reply, got)
		}
	}
	if !worthRetrieving(&job{}) {
		t.Error("a spontaneous job should retrieve")
	}
	// Short lines and commands are not archived.
	say(b, 10, ali, "/faal")
	say(b, 11, ali, "باشه")
	if _, err := db.GetArchived(testChat, 10); err == nil {
		t.Error("a command was archived")
	}
	if _, err := db.GetArchived(testChat, 11); err == nil {
		t.Error("a short message was archived")
	}
}

func TestPurgeArchiveOnIgnoreAndForget(t *testing.T) {
	b, db, _, _ := replyBot(t, 10)
	enableRAG(b)
	say(b, 1, ali, "ماشینمو فروختم، پراید سفید بود و خیلی دوستش داشتم")
	say(b, 2, sara, "چه حیف، همون پراید سفید که باهاش شمال رفتیم؟")
	flushAll(b)
	b.purgeArchive(testChat, ali.ID)
	if _, err := db.GetArchived(testChat, 1); err == nil {
		t.Error("Ali's archive survived the purge")
	}
	if _, err := db.GetArchived(testChat, 2); err != nil {
		t.Error("Sara's archive was purged")
	}
	if _, err := db.MsgVector(testChat, 1); err == nil {
		t.Error("Ali's vector survived the purge")
	}
	if _, err := db.MsgVector(testChat, 2); err != nil {
		t.Error("Sara's vector was purged")
	}
}

func TestItemVectorsAndSelection(t *testing.T) {
	b, db, _, _ := replyBot(t, 10)
	enableRAG(b)
	addItems(db, testChat, ali.ID, store.KindFact, false, "طرفدار پرسپولیس است", "برنامه‌نویس گو است", "گربه دارد")
	addItems(db, testChat, ali.ID, store.KindOrder, true, "باهاش شوخی نکن")
	mem, _ := db.GetMemory(testChat, ali.ID)
	b.embedItems(testChat, ali.ID, mem.Items)
	flushAll(b)
	vecs, _ := db.ItemVectors(testChat, ali.ID)
	if len(vecs) != 4 {
		t.Fatalf("item vectors = %d, want 4", len(vecs))
	}
	q, ok := b.embedder.query(context.Background(), "پرسپولیس امشب بازی داره")
	if !ok {
		t.Fatal("query failed")
	}
	j := job{chatID: testChat, queryVec: q, queryOK: true, queried: true}
	items, ok := b.selectItems(j, ali.ID, mem, 40)
	if !ok || len(items) < 2 {
		t.Fatalf("selected %+v", items)
	}
	if items[0].Kind != store.KindOrder {
		t.Errorf("the order must always be selected first: %+v", items)
	}
	if items[1].Text != "طرفدار پرسپولیس است" {
		t.Errorf("the closest fact should come first: %+v", items)
	}
	// Re-embedding unchanged items is skipped.
	before := b.embedder.embedded
	b.embedItems(testChat, ali.ID, mem.Items)
	flushAll(b)
	if b.embedder.embedded != before {
		t.Error("unchanged items were embedded again")
	}
}

func TestPromptTrimsToTheContext(t *testing.T) {
	b, db, _, fake := replyBot(t, 10, llm.Response{Content: "x"})
	b.deps.Cfg.Bot.HistoryMessages = 30
	b.deps.Cfg.LLM.MaxTokens = 1024
	b.deps.Cfg.LLM.ContextTokens = 1024 + 1200 // tiny: the transcript must be cut
	for i := 1; i <= 25; i++ {
		say(b, i, sara, strings.Repeat("حرف ", 40))
	}
	addItems(db, testChat, ali.ID, store.KindFact, false, "عاشق فوتبال")
	m := say(b, 26, ali, "هوشنگ چه خبر؟")
	b.runReply(context.Background(), job{kind: jobReply, chatID: testChat, msg: m})
	p := fake.Calls[0].Messages[1].Content
	if n := strings.Count(p, "سارا (@sara)"); n > 10 {
		t.Errorf("transcript was not trimmed: %d long lines", n)
	}
	if !strings.Contains(p, ">> [26]") {
		t.Errorf("the trigger must survive trimming:\n%s", p)
	}
}

func TestStyleIsCounted(t *testing.T) {
	b, db, _, _ := replyBot(t, 10)
	for i := 1; i <= 3; i++ {
		say(b, i, ali, "خخخ 😂 چه باحال")
	}
	m := say(b, 4, ali, "/faal")
	_ = m
	mem, _ := db.GetMember(testChat, ali.ID)
	if mem.Style.Msgs != 3 || mem.Style.Words != 12 || mem.Style.Emoji != 3 || mem.Style.Laughs["خخخ"] != 3 {
		t.Fatalf("style = %+v", mem.Style)
	}
}
