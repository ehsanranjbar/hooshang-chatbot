package memory

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/ehsanranjbar/hooshang/internal/store"
)

func TestApplyAddItem(t *testing.T) {
	p := store.Profile{}
	out, changes, err := Apply(p, []Op{{Action: "add", Section: "facts", Text: "lives in Shiraz"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Facts) != 1 || out.Facts[0].Text != "lives in Shiraz" || !out.Facts[0].Locked {
		t.Fatalf("add failed: %+v", out.Facts)
	}
	if len(changes) != 1 || !strings.Contains(changes[0], "add facts") {
		t.Fatalf("changes = %v", changes)
	}
}

func TestApplyAddNickname(t *testing.T) {
	p := store.Profile{}
	out, _, err := Apply(p, []Op{{Action: "add", Section: "nickname", Text: "آقا چای"}})
	if err != nil {
		t.Fatal(err)
	}
	if out.Nickname != "آقا چای" {
		t.Fatalf("nickname = %q", out.Nickname)
	}
}

func TestApplyRemove(t *testing.T) {
	p := store.Profile{Facts: []store.Item{{Text: "lives in Tehran"}, {Text: "likes tea"}}}
	out, changes, err := Apply(p, []Op{{Action: "remove", Section: "facts", OldText: "Tehran"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Facts) != 1 || out.Facts[0].Text != "likes tea" {
		t.Fatalf("remove failed: %+v", out.Facts)
	}
	if len(out.Suppressed) != 1 || !strings.Contains(out.Suppressed[0], "Tehran") {
		t.Fatalf("suppressed = %v", out.Suppressed)
	}
	if len(changes) != 1 || !strings.Contains(changes[0], "remove") {
		t.Fatalf("changes = %v", changes)
	}
}

func TestApplyReplace(t *testing.T) {
	p := store.Profile{Facts: []store.Item{{Text: "lives in Tehran"}}}
	out, _, err := Apply(p, []Op{{Action: "replace", Section: "facts", OldText: "Tehran", Text: "lives in Shiraz"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Facts) != 1 || out.Facts[0].Text != "lives in Shiraz" || !out.Facts[0].Locked {
		t.Fatalf("replace failed: %+v", out.Facts)
	}
}

func TestApplyRemoveMatchesNothing(t *testing.T) {
	p := store.Profile{Facts: []store.Item{{Text: "likes tea"}}}
	_, _, err := Apply(p, []Op{{Action: "remove", Section: "facts", OldText: "cooking"}})
	if err == nil || !strings.Contains(err.Error(), "no facts matches") {
		t.Fatalf("err = %v, want a named no-match error", err)
	}
}

func TestApplyUnknownSection(t *testing.T) {
	_, _, err := Apply(store.Profile{}, []Op{{Action: "add", Section: "hobbies", Text: "x"}})
	if err == nil || !strings.Contains(err.Error(), "unknown section") {
		t.Fatalf("err = %v, want unknown section", err)
	}
}

func TestApplyUnknownAction(t *testing.T) {
	_, _, err := Apply(store.Profile{}, []Op{{Action: "banish", Section: "facts", Text: "x"}})
	if err == nil || !strings.Contains(err.Error(), "unknown action") {
		t.Fatalf("err = %v, want unknown action", err)
	}
}

func TestApplyMultipleOps(t *testing.T) {
	p := store.Profile{Facts: []store.Item{{Text: "lives in Tehran"}, {Text: "likes tea"}}}
	out, changes, err := Apply(p, []Op{
		{Action: "remove", Section: "facts", OldText: "Tehran"},
		{Action: "add", Section: "facts", Text: "lives in Shiraz"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Facts) != 2 || len(changes) != 2 {
		t.Fatalf("out = %+v, changes = %v", out.Facts, changes)
	}
}

func TestApplyRemovePersianNormalization(t *testing.T) {
	p := store.Profile{Facts: []store.Item{{Text: "عاشق آشپزی"}}}
	out, _, err := Apply(p, []Op{{Action: "remove", Section: "facts", OldText: "آشپزی"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Facts) != 0 {
		t.Fatalf("normalize-sensitive remove failed: %+v", out.Facts)
	}
}

// The edit_memory tool sends snake_case keys; old_text must reach OldText.
func TestOpDecodesOldText(t *testing.T) {
	var ops []Op
	if err := json.Unmarshal([]byte(`[{"action":"replace","section":"facts","text":"در شیراز","old_text":"تهران"}]`), &ops); err != nil {
		t.Fatal(err)
	}
	p := store.Profile{Facts: []store.Item{{Text: "ساکن تهران"}}}
	got, _, err := Apply(p, ops)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Facts) != 1 || got.Facts[0].Text != "در شیراز" || !got.Facts[0].Locked {
		t.Fatalf("facts = %+v", got.Facts)
	}
}

func TestReplaceNicknameWithoutOldText(t *testing.T) {
	got, _, err := Apply(store.Profile{Nickname: "علی"}, []Op{{Action: "replace", Section: "nickname", Text: "دکتر"}})
	if err != nil {
		t.Fatal(err)
	}
	if got.Nickname != "دکتر" || !got.NicknameLocked {
		t.Fatalf("nickname = %q locked=%v", got.Nickname, got.NicknameLocked)
	}
}
