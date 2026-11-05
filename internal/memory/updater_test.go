package memory

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/ehsanranjbar/hooshang/internal/llm"
	"github.com/ehsanranjbar/hooshang/internal/store"
	"github.com/ehsanranjbar/kvutil/kv/memkv"
)

func testDB(t *testing.T) *store.DB {
	t.Helper()
	db, err := store.OpenWith(memkv.New(), time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

// seedMember adds a member and n recent messages from them.
func seedMember(t *testing.T, db *store.DB, chatID, userID int64, n int) {
	t.Helper()
	if _, err := db.UpdateMember(chatID, userID, func(m *store.Member) {
		m.FirstName = "Ali"
		m.MsgsSinceUpdate = n
	}); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < n; i++ {
		if err := db.AddMessage(chatID, int64(i+1), &store.Message{
			UserID: userID, Name: "Ali", Text: "hello " + string(rune('a'+i%26)), Date: time.Now(),
		}); err != nil {
			t.Fatal(err)
		}
	}
}

func TestUpdateNowMergesValidJSON(t *testing.T) {
	const chat, user = int64(-1), int64(7)
	db := testDB(t)
	seedMember(t, db, chat, user, 15)
	// Seed a locked item the extractor will "forget".
	db.UpdateMember(chat, user, func(m *store.Member) {
		m.Profile.Facts = append(m.Profile.Facts, store.Item{Text: "locked fact", Locked: true})
	})

	fake := &llm.Fake{}
	fake.Queue(llm.Response{Content: `{"nickname":"آقا چای","summary":"دوست‌داشتنی","facts":["new fact"]}`})
	u := NewUpdater(db, fake, "model", UpdaterConfig{ContextMessages: 150, MaxItems: 10, Language: "fa"}, nil)

	if err := u.UpdateNow(context.Background(), chat, user); err != nil {
		t.Fatal(err)
	}
	m, err := db.GetMember(chat, user)
	if err != nil {
		t.Fatal(err)
	}
	if m.Profile.Nickname == "" {
		t.Fatalf("profile not saved: %+v", m.Profile)
	}
	if !containsItem(m.Profile.Facts, "new fact") {
		t.Fatalf("extracted fact missing: %+v", m.Profile.Facts)
	}
	if !containsItem(m.Profile.Facts, "locked fact") {
		t.Fatalf("locked fact was dropped: %+v", m.Profile.Facts)
	}
	if m.MsgsSinceUpdate != 0 || m.ProfileUpdatedAt.IsZero() {
		t.Fatalf("counter/timestamp not reset: %+v", m)
	}
	// The request used temperature 0.2 and, by default, no schema.
	if len(fake.Calls) != 1 {
		t.Fatalf("calls = %d, want 1", len(fake.Calls))
	}
	req := fake.Calls[0]
	if req.Temperature != 0.2 || req.Schema != nil {
		t.Fatalf("bad request: %+v", req)
	}
	if req.MaxTokens < 2048 {
		t.Fatalf("MaxTokens = %d, want >= 2048", req.MaxTokens)
	}
	// The prompt spells out the exact JSON shape with an example.
	prompt := req.Messages[0].Content
	for _, want := range []string{`"nickname"`, `"facts"`, `{"nickname":"آقا چای"`, ">>"} {
		if !strings.Contains(prompt, want) {
			t.Errorf("prompt missing %q", want)
		}
	}
}

func TestUpdateNowKeepsProfileOnGarbage(t *testing.T) {
	const chat, user = int64(-1), int64(7)
	db := testDB(t)
	seedMember(t, db, chat, user, 15)
	db.UpdateMember(chat, user, func(m *store.Member) {
		m.Profile.Facts = append(m.Profile.Facts, store.Item{Text: "old fact"})
	})

	fake := &llm.Fake{}
	fake.Queue(llm.Response{Content: "I cannot return JSON, sorry"})
	u := NewUpdater(db, fake, "model", UpdaterConfig{ContextMessages: 150, MaxItems: 10, Language: "fa"}, nil)

	if err := u.UpdateNow(context.Background(), chat, user); err != nil {
		t.Fatal(err) // a parse failure is not an error
	}
	m, _ := db.GetMember(chat, user)
	if len(m.Profile.Facts) != 1 || m.Profile.Facts[0].Text != "old fact" {
		t.Fatalf("profile changed on garbage: %+v", m.Profile)
	}
	if m.MsgsSinceUpdate != 0 {
		t.Fatalf("counter not reset: %d", m.MsgsSinceUpdate)
	}
}

func TestUpdateNowKeepsProfileOnNoKnownKey(t *testing.T) {
	const chat, user = int64(-1), int64(7)
	db := testDB(t)
	seedMember(t, db, chat, user, 15)
	db.UpdateMember(chat, user, func(m *store.Member) {
		m.Profile.Facts = append(m.Profile.Facts, store.Item{Text: "old fact"})
	})

	fake := &llm.Fake{}
	fake.Queue(llm.Response{Content: `{"2+2": 4}`}) // valid JSON, no known key
	u := NewUpdater(db, fake, "model", UpdaterConfig{ContextMessages: 150, MaxItems: 10, Language: "fa"}, nil)

	if err := u.UpdateNow(context.Background(), chat, user); err != nil {
		t.Fatal(err)
	}
	m, _ := db.GetMember(chat, user)
	if len(m.Profile.Facts) != 1 || m.Profile.Facts[0].Text != "old fact" {
		t.Fatalf("profile changed on unknown-key JSON: %+v", m.Profile)
	}
	if m.MsgsSinceUpdate != 0 {
		t.Fatalf("counter not reset: %d", m.MsgsSinceUpdate)
	}
}

func TestUpdateNowFewMessages(t *testing.T) {
	const chat, user = int64(-1), int64(7)
	db := testDB(t)
	seedMember(t, db, chat, user, 2) // fewer than 3
	db.UpdateMember(chat, user, func(m *store.Member) { m.MsgsSinceUpdate = 9 })

	fake := &llm.Fake{}
	u := NewUpdater(db, fake, "model", UpdaterConfig{ContextMessages: 150, MaxItems: 10, Language: "fa"}, nil)
	if err := u.UpdateNow(context.Background(), chat, user); err != nil {
		t.Fatal(err)
	}
	if len(fake.Calls) != 0 {
		t.Fatalf("no LLM call expected, got %d", len(fake.Calls))
	}
	m, _ := db.GetMember(chat, user)
	if m.MsgsSinceUpdate != 0 {
		t.Fatalf("counter not reset: %d", m.MsgsSinceUpdate)
	}
}

func TestUpdateNowMissingMember(t *testing.T) {
	db := testDB(t)
	fake := &llm.Fake{}
	u := NewUpdater(db, fake, "model", UpdaterConfig{}, nil)
	// A missing member is a no-op, not an error.
	if err := u.UpdateNow(context.Background(), -1, 99); err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if len(fake.Calls) != 0 {
		t.Fatalf("no LLM call expected, got %d", len(fake.Calls))
	}
}

func TestEnqueueDedupsAndDropsWhenFull(t *testing.T) {
	db := testDB(t)
	fake := &llm.Fake{}
	u := NewUpdater(db, fake, "model", UpdaterConfig{}, nil)
	u.Enqueue(-1, 7)
	u.Enqueue(-1, 7) // duplicate
	if u.QueueLen() != 1 {
		t.Fatalf("QueueLen = %d, want 1 (dedup)", u.QueueLen())
	}
	// Fill the queue (cap 64).
	for i := 1; i < 64; i++ {
		u.Enqueue(-1, int64(i+10))
	}
	if u.QueueLen() != 64 {
		t.Fatalf("QueueLen = %d, want 64", u.QueueLen())
	}
	u.Enqueue(-1, 9999) // full: dropped
	if u.QueueLen() != 64 {
		t.Fatalf("QueueLen = %d after drop, want 64", u.QueueLen())
	}
}

func TestUpdateNowSendsSchemaWhenEnabled(t *testing.T) {
	const chat, user = int64(-1), int64(7)
	db := testDB(t)
	seedMember(t, db, chat, user, 15)
	fake := &llm.Fake{}
	fake.Queue(llm.Response{Content: `{"facts":["x"]}`})
	u := NewUpdater(db, fake, "model", UpdaterConfig{ContextMessages: 150, MaxItems: 10, Language: "fa", JSONSchema: true}, nil)
	if err := u.UpdateNow(context.Background(), chat, user); err != nil {
		t.Fatal(err)
	}
	if fake.Calls[0].Schema == nil || fake.Calls[0].SchemaName != "profile" {
		t.Fatal("schema not sent with JSONSchema: true")
	}
}
