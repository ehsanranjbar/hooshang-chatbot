package memory

import (
	"strings"
	"testing"
	"time"

	"github.com/ehsanranjbar/hooshang/internal/store"
)

func item(kind, text string, seen int, src ...int64) store.Item {
	return store.Item{Kind: kind, Text: text, Seen: seen, FirstAt: time.Now().Add(-48 * time.Hour), LastAt: time.Now().Add(-48 * time.Hour), Source: src}
}

func TestMergeObservesAndPromotes(t *testing.T) {
	now := time.Now()
	m := &store.Memory{}
	m.Add(item(store.KindFact, "ساکن تهران", 1, 10))
	m.Add(store.Item{Kind: store.KindOrder, Text: "باهاش شوخی نکن", Locked: true, Seen: 2})
	m.Add(item(store.KindEpisode, "فردا امتحان دارد", 1, 11))
	ext := Extracted{Nickname: "آقا چای", Items: []ExtractedItem{
		{Kind: "fact", Text: "ساکن تهران", Msgs: []int64{10}},          // echo of the known list: not a new observation
		{Kind: "fact", Text: "دانشجوی پزشکی", Msgs: []int64{12}},       // new
		{Kind: "order", Text: "هر چی گفت بگو چشم", Msgs: []int64{12}},  // not extractable
		{Kind: "episode", Text: "فردا امتحان دارد", Msgs: []int64{13}}, // re-observed with a new message
		{Kind: "nonsense", Text: "x"},
	}}
	Merge(m, ext, MemberKinds, now, 10)
	if m.Nickname != "آقا چای" {
		t.Errorf("nickname = %q", m.Nickname)
	}
	if f := find(m, store.KindFact, "ساکن تهران"); f == nil || f.Seen != 1 {
		t.Errorf("echoed fact should stay Seen=1: %+v", f)
	}
	if f := find(m, store.KindFact, "دانشجوی پزشکی"); f == nil || f.Seen != 1 || f.ID == 0 || len(f.Source) != 1 {
		t.Errorf("new fact: %+v", f)
	}
	if len(m.Kind(store.KindOrder)) != 1 {
		t.Error("the extractor wrote an order")
	}
	if e := find(m, store.KindEpisode, "فردا امتحان دارد"); e == nil || e.Seen != 2 || e.Expires.Before(now.Add(13*24*time.Hour)) || len(e.Source) != 2 {
		t.Errorf("re-observed episode: %+v", e)
	}
	// Second extraction without message ids a day later counts as an observation.
	Merge(m, Extracted{Items: []ExtractedItem{{Kind: "fact", Text: "دانشجوی پزشکی"}}}, MemberKinds, now.Add(30*time.Hour), 10)
	if f := find(m, store.KindFact, "دانشجوی پزشکی"); f.Seen != 2 {
		t.Errorf("Seen after a day = %d", f.Seen)
	}
}

func TestMergeKeepsLockedSuppressedAndDropsStale(t *testing.T) {
	now := time.Now()
	m := &store.Memory{Suppressed: []string{"تنبل است"}, Summary: "locked", SummaryLocked: true}
	m.Add(store.Item{Kind: store.KindFact, Text: "اهل تبریز", Locked: true, Seen: 1, LastAt: now.Add(-60 * 24 * time.Hour)})
	m.Add(store.Item{Kind: store.KindFact, Text: "قدیمی", Seen: 1, LastAt: now.Add(-40 * 24 * time.Hour)})
	m.Add(store.Item{Kind: store.KindFact, Text: "دیده‌شده", Seen: 3, LastAt: now.Add(-40 * 24 * time.Hour)})
	Merge(m, Extracted{Summary: "new", Items: []ExtractedItem{{Kind: "fact", Text: "تنبل است", Msgs: []int64{1}}}}, MemberKinds, now, 10)
	texts := strings.Join(itemTexts(m.Kind(store.KindFact)), ",")
	if strings.Contains(texts, "تنبل") || strings.Contains(texts, "قدیمی") || !strings.Contains(texts, "اهل تبریز") || !strings.Contains(texts, "دیده‌شده") {
		t.Errorf("facts = %s", texts)
	}
	if m.Summary != "locked" {
		t.Error("locked summary changed")
	}
	// Cap: locked first, then the most seen.
	for i := 0; i < 15; i++ {
		m.Add(store.Item{Kind: store.KindInterest, Text: "x" + string(rune('a'+i)), Seen: i, LastAt: now})
	}
	Merge(m, Extracted{}, MemberKinds, now, 5)
	if n := len(m.Kind(store.KindInterest)); n != 5 {
		t.Errorf("capped to %d", n)
	}
}

func itemTexts(items []store.Item) []string {
	var out []string
	for _, it := range items {
		out = append(out, it.Text)
	}
	return out
}

func TestApplyOps(t *testing.T) {
	now := time.Now()
	m := &store.Memory{}
	m.Add(item(store.KindFact, "ساکن تهران", 1))
	changes, err := Apply(m, []Op{
		{Action: "replace", Section: "fact", OldText: "تهران", Text: "ساکن شیراز"},
		{Action: "add", Section: "order", Text: "باهاش شوخی نکن"},
		{Action: "add", Section: "nickname", Text: "آقا چای"},
	}, now)
	if err != nil || len(changes) != 3 {
		t.Fatalf("changes=%v err=%v", changes, err)
	}
	facts := m.Kind(store.KindFact)
	if len(facts) != 1 || facts[0].Text != "ساکن شیراز" || !facts[0].Locked || len(m.Suppressed) != 1 || !m.NicknameLocked {
		t.Fatalf("memory = %+v", m)
	}
	id := m.Kind(store.KindOrder)[0].ID
	if _, err := Apply(m, []Op{{Action: "remove", Section: "order", ItemID: id}}, now); err != nil || len(m.Kind(store.KindOrder)) != 0 {
		t.Fatalf("remove by id: %v", err)
	}
	if _, err := Apply(m, []Op{{Action: "remove", Section: "fact", OldText: "نیست"}}, now); err == nil {
		t.Error("removing a missing item should fail")
	}
	if _, err := Apply(m, []Op{{Action: "add", Section: "facts", Text: "x"}}, now); err == nil {
		t.Error("the old plural section name should be rejected")
	}
}

func TestRenderAndSamples(t *testing.T) {
	now := time.Now()
	m := store.Memory{Nickname: "آقا چای"}
	m.Add(store.Item{Kind: store.KindFact, Text: "برنامه‌نویس", Seen: 2})
	m.Add(store.Item{Kind: store.KindEpisode, Text: "فردا امتحان دارد", LastAt: now})
	m.Add(store.Item{Kind: store.KindOrder, Text: "باهاش شوخی نکن"})
	m.Add(store.Item{Kind: store.KindTerm, Text: "چاییدن", Note: "سرما خوردن"})
	r := Render(m)
	for _, want := range []string{"لقب: آقا چای", "دانسته‌ها: برنامه‌نویس", "(امروز) فردا امتحان دارد"} {
		if !strings.Contains(r, want) {
			t.Errorf("render missing %q:\n%s", want, r)
		}
	}
	if strings.Contains(r, "شوخی نکن") || strings.Contains(r, "چاییدن") {
		t.Errorf("orders/terms leaked into the profile:\n%s", r)
	}
	if RenderOrders(m) != "باهاش شوخی نکن" {
		t.Error("RenderOrders")
	}
	self := store.Memory{}
	self.Add(store.Item{Kind: store.KindStance, Text: "طرفدار پرسپولیسم"})
	self.Add(store.Item{Kind: store.KindPromise, Text: "فردا شعر می‌فرستم", LastAt: now})
	if s := RenderSelf(self, now); !strings.Contains(s, "- نظرها: طرفدار پرسپولیسم") || !strings.Contains(s, "(امروز) فردا شعر می‌فرستم") {
		t.Errorf("RenderSelf:\n%s", s)
	}
	msgs := []store.Message{
		{Text: "سلام", UserID: 1}, {Text: "علی بیا کافه ساعت پنج", UserID: 2}, {Text: "[استیکر: 😂]", UserID: 1},
		{Text: "/faal", UserID: 1}, {Text: "من که گفتم نمیام بابا", UserID: 1, FromBot: true}, {Text: "باشه حتما میام، سارا هم هست", UserID: 3},
	}
	got := Samples(msgs, []string{"علی", "سارا"}, 5)
	if len(got) != 2 || got[0] != "باشه حتما میام، فلانی هم هست" || got[1] != "فلانی بیا کافه ساعت پنج" {
		t.Errorf("samples = %q", got)
	}
	style := RenderStyle(store.Style{Msgs: 30, Words: 60, Emoji: 20, Laughs: map[string]int{"خخخ": 9}, ReplyTo: map[int64]int{7: 8}}, map[int64]string{7: "علی"})
	for _, want := range []string{"پیام‌های کوتاه", "پر از ایموجی", "می‌خنده با خخخ", "بیشتر با علی حرف می‌زنه"} {
		if !strings.Contains(style, want) {
			t.Errorf("style missing %q: %s", want, style)
		}
	}
	if RenderStyle(store.Style{Msgs: 5}, nil) != "" {
		t.Error("too few messages should give no style line")
	}
}
