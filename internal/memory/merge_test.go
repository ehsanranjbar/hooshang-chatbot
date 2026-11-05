package memory

import (
	"strings"
	"testing"

	"github.com/ehsanranjbar/hooshang/internal/store"
)

func TestMergeKeepsLockedItems(t *testing.T) {
	current := store.Profile{
		Nickname: "آقا چای",
		Facts:    []store.Item{{Text: "Hamadan", Locked: true}},
	}
	extracted := store.Profile{
		Nickname: "آقا چای",
		Facts:    []store.Item{{Text: "new fact"}}, // the extractor dropped the locked item
	}
	got := Merge(current, extracted, 10)
	found := false
	for _, it := range got.Facts {
		if it.Text == "Hamadan" && it.Locked {
			found = true
		}
	}
	if !found {
		t.Fatalf("locked item was not kept: %+v", got.Facts)
	}
	if !containsItem(got.Facts, "new fact") {
		t.Fatal("extracted item missing")
	}
}

func TestMergeDropsSuppressed(t *testing.T) {
	current := store.Profile{
		Suppressed: []string{"عاشق آشپزی"},
	}
	extracted := store.Profile{
		Facts: []store.Item{{Text: "عاشق آشپزی"}, {Text: "دوست دارد چای"}, {Text: "عاشق آشپزی است"}},
	}
	got := Merge(current, extracted, 10)
	// The exact suppressed text is dropped; a different fact that merely
	// contains it is kept (Merge matches by normalized equality).
	for _, it := range got.Facts {
		if Normalize(it.Text) == Normalize("عاشق آشپزی") {
			t.Fatalf("suppressed item survived: %+v", got.Facts)
		}
	}
	if len(got.Facts) != 2 {
		t.Fatalf("wrong facts: %+v", got.Facts)
	}
}

func TestMergeDedups(t *testing.T) {
	extracted := store.Profile{
		Facts: []store.Item{{Text: "same"}, {Text: "same"}, {Text: "other"}},
	}
	got := Merge(store.Profile{}, extracted, 10)
	if len(got.Facts) != 2 {
		t.Fatalf("dedup failed: %+v", got.Facts)
	}
}

func TestMergeCapsAtMaxItems(t *testing.T) {
	var items []store.Item
	for i := 0; i < 15; i++ {
		items = append(items, store.Item{Text: string(rune('a'+i)) + "-fact"})
	}
	extracted := store.Profile{Facts: items}
	got := Merge(store.Profile{}, extracted, 10)
	if len(got.Facts) != 10 {
		t.Fatalf("cap failed: got %d items, want 10", len(got.Facts))
	}
}

func TestMergeLockedFirstInCap(t *testing.T) {
	var items []store.Item
	for i := 0; i < 12; i++ {
		items = append(items, store.Item{Text: string(rune('a'+i)) + "-fact"})
	}
	items[0] = store.Item{Text: "locked-fact", Locked: true}
	extracted := store.Profile{Facts: items}
	got := Merge(store.Profile{}, extracted, 3)
	if len(got.Facts) != 3 {
		t.Fatalf("cap failed: got %d items", len(got.Facts))
	}
	if got.Facts[0].Text != "locked-fact" || !got.Facts[0].Locked {
		t.Fatalf("locked item should be first: %+v", got.Facts)
	}
}

func TestMergeCopiesSuppressed(t *testing.T) {
	current := store.Profile{Suppressed: []string{"old"}}
	extracted := store.Profile{Suppressed: []string{"should-be-dropped"}}
	got := Merge(current, extracted, 10)
	if len(got.Suppressed) != 1 || got.Suppressed[0] != "old" {
		t.Fatalf("Suppressed = %v, want [old]", got.Suppressed)
	}
}

func TestMergeLockedItemKeepsItsSection(t *testing.T) {
	current := store.Profile{
		Interests: []store.Item{{Text: "کوهنوردی", Locked: true}},
	}
	extracted := store.Profile{} // extractor lost it entirely
	got := Merge(current, extracted, 10)
	if len(got.Interests) != 1 || got.Interests[0].Text != "کوهنوردی" || !got.Interests[0].Locked {
		t.Fatalf("locked interest not kept: %+v", got.Interests)
	}
}

func TestMergeKeepsAdminNicknameAndSummary(t *testing.T) {
	cur := store.Profile{Nickname: "دکتر", NicknameLocked: true, Summary: "s", SummaryLocked: true}
	got := Merge(cur, store.Profile{Nickname: "علی‌جون", Summary: "other"}, 10)
	if got.Nickname != "دکتر" || !got.NicknameLocked || got.Summary != "s" || !got.SummaryLocked {
		t.Fatalf("got %+v", got)
	}
}

func TestMergeEmptyExtractedKeepsCurrentStrings(t *testing.T) {
	got := Merge(store.Profile{Nickname: "آقا چای", Summary: "پرانرژی"}, store.Profile{}, 10)
	if got.Nickname != "آقا چای" || got.Summary != "پرانرژی" {
		t.Fatalf("got %+v", got)
	}
}

func TestMergeDropsSuppressedNickname(t *testing.T) {
	got := Merge(store.Profile{Suppressed: []string{"کچل"}}, store.Profile{Nickname: "کچل"}, 10)
	if got.Nickname != "" {
		t.Fatalf("suppressed nickname came back: %q", got.Nickname)
	}
}

func TestMergeKeepsOrders(t *testing.T) {
	cur := store.Profile{Orders: []store.Item{{Text: "باهاش شوخی نکن", Locked: true}}}
	got := Merge(cur, store.Profile{Facts: []store.Item{{Text: "x"}}}, 10)
	if len(got.Orders) != 1 || got.Orders[0].Text != "باهاش شوخی نکن" {
		t.Fatalf("orders = %+v", got.Orders)
	}
	if strings.Contains(Render(got), "شوخی نکن") {
		t.Errorf("orders leaked into Render: %s", Render(got))
	}
}

func TestMergeKeepsTheLockOnAReproducedItem(t *testing.T) {
	current := store.Profile{Facts: []store.Item{{Text: "Hamadan", Locked: true}}}
	extracted := store.Profile{Facts: []store.Item{{Text: "Hamadan"}, {Text: "other"}}} // the extractor echoed it, unlocked
	got := Merge(current, extracted, 10)
	n := 0
	for _, it := range got.Facts {
		if it.Text == "Hamadan" {
			n++
			if !it.Locked {
				t.Fatalf("the lock was lost: %+v", got.Facts)
			}
		}
	}
	if n != 1 {
		t.Fatalf("Hamadan appears %d times: %+v", n, got.Facts)
	}
	if extracted.Facts[0].Locked {
		t.Fatal("Merge wrote into the caller's slice")
	}
}
