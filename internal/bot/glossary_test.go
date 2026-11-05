package bot

import (
	"context"
	"strings"
	"testing"

	"github.com/ehsanranjbar/hooshang/internal/config"
	"github.com/ehsanranjbar/hooshang/internal/llm"
	"github.com/ehsanranjbar/hooshang/internal/store"
)

func enableGlossary(b *Bot) {
	b.deps.Cfg.Features.Glossary = &config.GlossaryFeature{Enabled: true, MaxItems: 100, InPrompt: 8}
}

func TestLearnTermByAnyone(t *testing.T) {
	b, db, tg, _ := replyBot(t, 10,
		llm.Response{ToolCalls: []llm.ToolCall{{ID: "1", Name: "learn_term", Arguments: `{"term":"«چاییدن»","meaning":"سرما خوردن"}`}}},
		llm.Response{Content: "گرفتم، چاییدی پس"},
	)
	enableGlossary(b)
	m := say(b, 1, sara, `هوشنگ، "چاییدن" یعنی سرما خوردن`)
	b.runReply(context.Background(), job{kind: jobReply, chatID: testChat, msg: m})
	got, err := db.GetTerm(testChat, "چاییدن")
	if err != nil || got.Meaning != "سرما خوردن" || got.ByID != sara.ID {
		t.Fatalf("term = %+v, %v", got, err)
	}
	if len(tg.sent) != 1 {
		t.Fatalf("sent %d messages", len(tg.sent))
	}
}

func TestForgetTermPermission(t *testing.T) {
	b, db, _, _ := replyBot(t, 10)
	enableGlossary(b)
	_, _ = db.PutTerm(testChat, store.Term{Term: "قلقلی", Meaning: "آدم حساس", ByID: sara.ID, ByName: "سارا"}, 100)

	forget := b.toolForgetTerm(job{chatID: testChat, msg: say(b, 2, ali, "قلقلی رو یادت بره")})
	if out, _ := forget(context.Background(), `{"term":"قلقلی"}`); !strings.Contains(out, "error") {
		t.Fatalf("a stranger forgot the term: %s", out)
	}
	forget = b.toolForgetTerm(job{chatID: testChat, msg: say(b, 3, sara, "قلقلی رو یادت بره")})
	if out, _ := forget(context.Background(), `{"term":"قلقلی"}`); strings.Contains(out, "error") {
		t.Fatalf("the teacher could not forget: %s", out)
	}
	if _, err := db.GetTerm(testChat, "قلقلی"); err == nil {
		t.Fatal("still there")
	}
}

func TestGlossaryInPrompt(t *testing.T) {
	b, db, _, fake := replyBot(t, 10, llm.Response{Content: "x"})
	enableGlossary(b)
	_, _ = db.PutTerm(testChat, store.Term{Term: "قلقلی", Meaning: "آدم حساس"}, 100)
	_, _ = db.PutTerm(testChat, store.Term{Term: "لب", Meaning: "نباید بیاد"}, 100) // only inside a word below
	_, _ = db.PutTerm(testChat, store.Term{Term: "کوه‌نوردی", Meaning: "نباید بیاد"}, 100)
	before, _ := db.GetTerm(testChat, "قلقلی")
	_, _ = db.PutTerm(testChat, store.Term{Term: "چاییدن", Meaning: "سرما خوردن"}, 100)
	m := say(b, 1, ali, "هوشنگ این علی خیلی قلقلی‌ه، مطلب داری؟ من که چاییدم")
	b.runReply(context.Background(), job{kind: jobReply, chatID: testChat, msg: m})
	p := fake.Calls[0].Messages[1].Content
	if !strings.Contains(p, "- «قلقلی»: آدم حساس") {
		t.Errorf("term missing from the prompt:\n%s", p)
	}
	if !strings.Contains(p, "- «چاییدن»: سرما خوردن") {
		t.Errorf("an inflected verb term is missing from the prompt:\n%s", p)
	}
	if strings.Contains(p, "نباید بیاد") {
		t.Errorf("unrelated term in the prompt:\n%s", p)
	}
	if after, _ := db.GetTerm(testChat, "قلقلی"); !after.UsedAt.After(before.UsedAt) {
		t.Error("a shown term was not marked used")
	}
}

func TestTermMatchesVerbForms(t *testing.T) {
	norm := store.NormalizeTerm
	for _, c := range []struct {
		text, term string
		want       bool
	}{
		{"من بدجور چاییدم", "چاییدن", true},
		{"تو نچاییدی؟", "چاییدن", true},
		{"داشتم می‌چاییدم", "چاییدن", true},
		{"از خنده رید", "ریدن", true},
		{"نمی‌ریدی اگه", "ریدن", true},
		{"دیروز خرید کردم", "ریدن", false},
		{"دیشب حسابی حال کردیم", "حال کردن", true},
		{"خسته‌ام", "ریدن", false},
		{"نون خریدم", "نون", true},
	} {
		if got := termMatches(norm(c.text), norm(c.term)); got != c.want {
			t.Errorf("termMatches(%q, %q) = %v", c.text, c.term, got)
		}
	}
}

func TestContainsTerm(t *testing.T) {
	for _, c := range []struct {
		text, term string
		want       bool
	}{
		{"قلقلی بازی", "قلقلی", true},
		{"این قلقلی ها", "قلقلی", true},
		{"مطلب", "لب", false},
		{"«لب» گفت", "لب", true},
		{"hooshi mode on", "hooshi mode", true},
	} {
		if got := containsTerm(c.text, c.term); got != c.want {
			t.Errorf("containsTerm(%q, %q) = %v", c.text, c.term, got)
		}
	}
}
