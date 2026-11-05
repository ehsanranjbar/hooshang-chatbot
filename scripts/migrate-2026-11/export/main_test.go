package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ehsanranjbar/kvutil/kv/memkv"

	"github.com/ehsanranjbar/hooshang/internal/store"
)

const chat = int64(-1001)

// fill stores one of everything.
func fill(t *testing.T, db *store.DB) {
	t.Helper()
	now := time.Now()
	check := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	check(db.UpdateChat(chat, func(c *store.Chat) {
		c.Title, c.Muted, c.ModActions = "گروه", true, map[string]bool{"delete": true, "pin": true}
	}))
	_, err := db.UpdateMember(chat, 7, func(m *store.Member) {
		m.FirstName, m.LastName, m.Username, m.NoTease = "علی", "رضایی", "ali", true
		m.ProfileUpdatedAt = now
		m.Profile = store.Profile{
			Nickname: "مهندس", NicknameLocked: true, Summary: "خلاصه", SummaryLocked: true,
			Facts:         []store.Item{{Text: "تهرانی", Locked: true}, {Text: "دانشجو"}},
			Interests:     []store.Item{{Text: "فوتبال"}},
			Quirks:        []store.Item{{Text: "دیر میاد"}},
			Relationships: []store.Item{{Text: "دوست سارا"}},
			Boundaries:    []store.Item{{Text: "شوخی با کار"}},
			Orders:        []store.Item{{Text: "مودب باش", Locked: true}},
			Suppressed:    []string{"شیرازی"},
		}
	})
	check(err)
	_, err = db.UpdateMember(chat, 8, func(m *store.Member) { m.FirstName, m.Ignored = "سارا", true })
	check(err)
	check(db.ReplaceGroupEvents(chat, []store.GroupEvent{{Text: "کوه‌نوردی", Score: 9, FirstAt: now.Add(-time.Hour), LastAt: now}}, time.Hour*24))
	_, err = db.PutTerm(chat, store.Term{Term: "چاییدن", Meaning: "سرما خوردن", ByID: 7, ByName: "علی"}, 10)
	check(err)
	check(db.PutMediaDesc(store.MediaDesc{UniqueID: "u1", Kind: "sticker", Text: "گربه", FileID: "f", Emoji: "😂", Uses: 3}, time.Hour))
}

// load reads a dump file back.
func load(t *testing.T, path string) Dump {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var d Dump
	if err := json.Unmarshal(b, &d); err != nil {
		t.Fatalf("dump is not valid JSON: %v", err)
	}
	return d
}

func checkDump(t *testing.T, d Dump) {
	t.Helper()
	if len(d.Chats) != 1 || d.Chats[0].ID != chat || d.Chats[0].Title != "گروه" || !d.Chats[0].Muted ||
		len(d.Chats[0].ModActions) != 2 || !d.Chats[0].ModActions["pin"] {
		t.Errorf("chats = %+v", d.Chats)
	}
	if len(d.Members) != 2 {
		t.Fatalf("members = %+v", d.Members)
	}
	ali, sara := d.Members[0], d.Members[1]
	p := ali.Profile
	if ali.Chat != chat || ali.User != 7 || ali.FirstName != "علی" || ali.LastName != "رضایی" || ali.Username != "ali" || !ali.NoTease || ali.Ignored ||
		ali.ProfileUpdatedAt.IsZero() {
		t.Errorf("member = %+v", ali)
	}
	if p.Nickname != "مهندس" || !p.NicknameLocked || p.Summary != "خلاصه" || !p.SummaryLocked ||
		len(p.Facts) != 2 || !p.Facts[0].Locked || p.Facts[1].Locked || len(p.Interests) != 1 || len(p.Quirks) != 1 ||
		len(p.Relationships) != 1 || len(p.Boundaries) != 1 || len(p.Orders) != 1 || !p.Orders[0].Locked ||
		len(p.Suppressed) != 1 || p.Suppressed[0] != "شیرازی" {
		t.Errorf("profile = %+v", p)
	}
	if !sara.Ignored || sara.FirstName != "سارا" {
		t.Errorf("ignored member = %+v", sara)
	}
	if len(d.Events) != 1 || d.Events[0].Chat != chat || d.Events[0].Text != "کوه‌نوردی" || d.Events[0].Score != 9 ||
		d.Events[0].FirstAt.IsZero() || d.Events[0].LastAt.IsZero() {
		t.Errorf("events = %+v", d.Events)
	}
	if len(d.Terms) != 1 || d.Terms[0].Term != "چاییدن" || d.Terms[0].Norm != "چاییدن" || d.Terms[0].Meaning != "سرما خوردن" ||
		d.Terms[0].ByID != 7 || d.Terms[0].ByName != "علی" || d.Terms[0].AddedAt.IsZero() || d.Terms[0].UsedAt.IsZero() {
		t.Errorf("terms = %+v", d.Terms)
	}
	if len(d.Media) != 1 || d.Media[0].UniqueID != "u1" || d.Media[0].Text != "گربه" || d.Media[0].Emoji != "😂" || d.Media[0].Uses != 3 {
		t.Errorf("media = %+v", d.Media)
	}
}

func TestExportInMemory(t *testing.T) {
	db, err := store.OpenWith(memkv.New(), time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	fill(t, db)
	d, err := build(db)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "dump.json")
	if err := write(path, d); err != nil {
		t.Fatal(err)
	}
	checkDump(t, load(t, path))
	if st, _ := os.Stat(path); st.Mode().Perm() != 0o600 {
		t.Errorf("dump permissions = %v: it holds private data", st.Mode().Perm())
	}
}

func TestRunReadsADataDirectory(t *testing.T) {
	dir := t.TempDir()
	db, err := store.Open(dir, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	fill(t, db)
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(t.TempDir(), "dump.json")
	if err := run(dir, out); err != nil {
		t.Fatal(err)
	}
	checkDump(t, load(t, out))

	// A missing directory is an error, and is not created.
	missing := filepath.Join(t.TempDir(), "nope")
	if err := run(missing, out); err == nil {
		t.Error("a missing data directory was accepted")
	}
	if _, err := os.Stat(missing); err == nil {
		t.Error("the missing directory was created")
	}
}
