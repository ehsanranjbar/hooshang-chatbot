package memory

import (
	"strings"
	"testing"

	"github.com/ehsanranjbar/hooshang/internal/store"
)

func TestSections(t *testing.T) {
	want := []string{"nickname", "summary", "facts", "interests", "quirks", "relationships", "boundaries", "orders"}
	got := Sections()
	if len(got) != len(want) {
		t.Fatalf("Sections = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("Sections[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestNormalize(t *testing.T) {
	// Built from unicode escapes so the test is unambiguous.
	ali := "\u0627\u0644\u064a"                                    // آلی with Arabic ي
	aliPersian := "\u0627\u0644\u06cc"                             // آلی with Persian ی
	zwnj := "\u062d\u0627\u200c\u0641\u0636"                       // حافظ with a ZWNJ
	spaces := "\u0633\u0644\u0627\u0645  \u062f\u0646\u06cc\u0627" // سلام  دنیا
	cases := [][2]string{
		{ali, aliPersian}, // ي→ی
		{zwnj, strings.ReplaceAll(zwnj, "\u200c", "")},
		{spaces, strings.ReplaceAll(spaces, " ", "")},
		{"HELLO", "hello"},
		{"", ""},
	}
	for _, c := range cases {
		if got := Normalize(c[0]); got != c[1] {
			t.Errorf("Normalize(%q) = %q, want %q", c[0], got, c[1])
		}
	}
}

func TestRenderOmitsEmptySections(t *testing.T) {
	nick := "آقا چای"
	chai := "دوست دارد چای بنوشد"
	p := store.Profile{
		Nickname: nick,
		Facts:    []store.Item{{Text: chai}},
	}
	out := Render(p)
	if !strings.Contains(out, nick) || !strings.Contains(out, chai) {
		t.Errorf("missing nickname or fact: %q", out)
	}
	// Empty sections must not appear.
	for _, label := range []string{"خلاصه", "علاقه‌مندی‌ها", "عادت‌ها", "رابطه‌ها", "مرزها"} {
		if strings.Contains(out, label) {
			t.Errorf("empty section %q was rendered: %q", label, out)
		}
	}
}

func TestRenderNeverShowsSuppressed(t *testing.T) {
	p := store.Profile{
		Summary:    "عاشق آشپزی",
		Suppressed: []string{"عاشق آشپزی"},
	}
	out := Render(p)
	if strings.Contains(out, "Suppressed") || strings.Contains(out, "suppressed") {
		t.Errorf("suppressed must not be rendered: %q", out)
	}
}

func TestRenderEmpty(t *testing.T) {
	if got := Render(store.Profile{}); got != "" {
		t.Errorf("empty profile should render empty, got %q", got)
	}
}

func TestRenderPersianSections(t *testing.T) {
	p := store.Profile{
		Nickname:   "نرگس",
		Summary:    "دوست‌داشتنی و پرانرژی",
		Interests:  []store.Item{{Text: "کوهنوردی"}},
		Boundaries: []store.Item{{Text: "با وزنم شوخی نکن"}},
	}
	out := Render(p)
	for _, want := range []string{"نرگس", "دوست‌داشتنی و پرانرژی", "کوهنوردی", "با وزنم شوخی نکن"} {
		if !strings.Contains(out, want) {
			t.Errorf("Render missing %q: %q", want, out)
		}
	}
}
