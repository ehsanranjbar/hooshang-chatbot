package bot

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/ehsanranjbar/hooshang/internal/llm"
	"github.com/ehsanranjbar/hooshang/internal/memory"
	"github.com/ehsanranjbar/hooshang/internal/store"
)

func TestMemberAnalysis(t *testing.T) {
	b, db, _, fake := replyBot(t, 10,
		llm.Response{Content: `{"nickname":"","summary":"برنامه‌نویس شوخ","items":[{"kind":"fact","text":"برنامه‌نویس است","msgs":[1]}]}`},
		llm.Response{Content: `{"analysis":"خلق و خو: پرانرژی و زودجوش.\nشوخی: از تیکه خوشش می‌آید، ولی روی قدش حساس است."}`},
		llm.Response{Content: `{"analysis":"خلق و خو: آرام‌تر شده."}`},
		llm.Response{Content: "x"},
	)
	enableRAG(b)
	_ = db.PutMediaDesc(store.MediaDesc{Text: "گربه‌ای که از خنده غش کرده", UniqueID: "u-stk", Kind: "sticker", FileID: "stk", Emoji: "😂"}, time.Hour)
	for i := 1; i <= 30; i++ {
		say(b, i, ali, "من برنامه‌نویسم و امروز کلی کد زدم، خسته شدم")
	}
	flushAll(b)
	_, _ = db.UpdateMember(testChat, ali.ID, func(m *store.Member) { m.Style.Media = map[string]int{"u-stk": 4} })
	addItems(db, testChat, ali.ID, store.KindBoundary, false, "شوخی با قدش نکن")

	upd := memory.NewUpdater(db, fake, "m", memory.UpdaterConfig{ContextMessages: 50, MaxItems: 5, Language: "fa",
		Analysis: memory.AnalysisConfig{Every: 10, MinMsgs: 30, MaxWords: 100}}, nil)
	if err := upd.UpdateNow(context.Background(), testChat, ali.ID); err != nil {
		t.Fatal(err)
	}
	if len(fake.Calls) != 2 {
		t.Fatalf("%d model calls, want the extraction and the analysis", len(fake.Calls))
	}
	p := fake.Calls[1].Messages[0].Content
	for _, want := range []string{ali.FirstName, "شوخی با قدش نکن", "برنامه‌نویس است", "گربه‌ای که از خنده غش کرده (×4)", "امروز کلی کد زدم", "100 words", "clinical"} {
		if !strings.Contains(p, want) {
			t.Errorf("analysis prompt missing %q:\n%s", want, p)
		}
	}
	mem, _ := db.GetMemory(testChat, ali.ID)
	if !strings.Contains(mem.Analysis, "زودجوش") || mem.AnalysisMsgs != 30 || mem.AnalysisAt.IsZero() {
		t.Fatalf("analysis not stored: %+v", mem)
	}
	// Not redone until Every more messages; then redone with the previous read in the prompt.
	if err := upd.AnalyseNowIfDue(context.Background(), testChat, ali.ID); err != nil || len(fake.Calls) != 2 {
		t.Fatalf("analysis redone too early: calls %d, err %v", len(fake.Calls), err)
	}
	for i := 31; i <= 40; i++ {
		say(b, i, ali, "باز هم کد")
	}
	if err := upd.AnalyseNowIfDue(context.Background(), testChat, ali.ID); err != nil || len(fake.Calls) != 3 {
		t.Fatalf("analysis not redone: calls %d, err %v", len(fake.Calls), err)
	}
	if p := fake.Calls[2].Messages[0].Content; !strings.Contains(p, "Your previous read") || !strings.Contains(p, "زودجوش") {
		t.Errorf("previous analysis missing from the prompt:\n%s", p)
	}
	mem, _ = db.GetMemory(testChat, ali.ID)
	if !strings.Contains(mem.Analysis, "آرام‌تر") || mem.AnalysisMsgs != 40 {
		t.Fatalf("second analysis: %+v", mem)
	}

	// The analysis is part of the member's rendered memory: in the reply prompt and in /me.
	m := say(b, 41, ali, "هوشنگ چه خبر؟")
	b.runReply(context.Background(), job{kind: jobReply, chatID: testChat, msg: m})
	if p := fake.Calls[len(fake.Calls)-1].Messages[1].Content; !strings.Contains(p, "تحلیل: خلق و خو: آرام‌تر شده.") {
		t.Errorf("analysis missing from the reply prompt:\n%s", p)
	}
	if r := memory.Render(mem); !strings.Contains(r, "تحلیل:") {
		t.Errorf("/me render: %s", r)
	}
}

func TestParseAnalysis(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{`{"analysis": "خوب"}`, "خوب"},
		{"<think>hmm</think>\n{\"analysis\":\"متن\"}", "متن"},
		{"خلق و خو: آرام", "خلق و خو: آرام"},
		{`{"analysis": ""}`, ""},
		{"", ""},
	} {
		got, err := memory.ParseAnalysis(c.in)
		if got != c.want || (err != nil) != (c.want == "") {
			t.Errorf("ParseAnalysis(%q) = %q, %v", c.in, got, err)
		}
	}
}
