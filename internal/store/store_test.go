package store

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/ehsanranjbar/kvutil/kv/memkv"
)

func engines(t *testing.T) map[string]*DB {
	mem, err := OpenWith(memkv.New(), time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	bdg, err := Open(t.TempDir(), time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { bdg.Close() })
	return map[string]*DB{"memkv": mem, "badger": bdg}
}

func TestStore(t *testing.T) {
	for name, db := range engines(t) {
		t.Run(name, func(t *testing.T) {
			const chat, other = int64(-1001234567890), int64(-42)

			// chats
			if c, err := db.GetChat(chat); err != nil || c.Muted {
				t.Fatal(c, err)
			}
			if err := db.UpdateChat(chat, func(c *Chat) { c.Muted = true; c.Title = "g" }); err != nil {
				t.Fatal(err)
			}
			if c, _ := db.GetChat(chat); !c.Muted || c.Title != "g" {
				t.Fatal(c)
			}

			// members + username index
			if _, err := db.GetMember(chat, 7); !errors.Is(err, ErrNotFound) {
				t.Fatal(err)
			}
			m, err := db.UpdateMember(chat, 7, func(m *Member) {
				m.FirstName, m.Username = "Ali", "AliR"
				m.Profile.Facts = append(m.Profile.Facts, Item{Text: "lives in Tehran", Locked: true})
			})
			if err != nil || m.UserID() != 7 || m.ChatID() != chat {
				t.Fatal(m, err)
			}
			db.UpdateMember(other, 7, func(m *Member) { m.Username = "alir" }) // same user, other chat
			db.UpdateMember(chat, 8, func(m *Member) { m.FirstName = "NoUsername" })

			got, err := db.MemberByUsername(chat, "@alir")
			if err != nil || got.FirstName != "Ali" || got.Profile.Facts[0].Text != "lives in Tehran" {
				t.Fatal(got, err)
			}
			// rename: the old username must no longer resolve
			db.UpdateMember(chat, 7, func(m *Member) { m.Username = "ali2" })
			if _, err := db.MemberByUsername(chat, "alir"); !errors.Is(err, ErrNotFound) {
				t.Fatal("old username still indexed", err)
			}
			if got, err := db.MemberByUsername(chat, "ALI2"); err != nil || got.UserID() != 7 {
				t.Fatal(got, err)
			}
			ms, err := db.Members(chat)
			if err != nil || len(ms) != 2 {
				t.Fatal(len(ms), err)
			}
			if err := db.DeleteMember(chat, 7); err != nil {
				t.Fatal(err)
			}
			if _, err := db.MemberByUsername(chat, "ali2"); !errors.Is(err, ErrNotFound) {
				t.Fatal("deleted member still indexed", err)
			}

			// messages: order and chat isolation
			for i := 1; i <= 50; i++ {
				db.AddMessage(chat, int64(i), &Message{UserID: 7, Text: fmt.Sprint(i)})
				db.AddMessage(other, int64(i), &Message{Text: "x"})
			}
			if m, err := db.GetMessage(chat, 7); err != nil || m.Text != "7" {
				t.Fatal(m, err)
			}
			if _, err := db.GetMessage(chat, 999); !errors.Is(err, ErrNotFound) {
				t.Fatal(err)
			}
			msgs, err := db.RecentMessages(chat, 5)
			if err != nil || len(msgs) != 5 || msgs[0].Text != "46" || msgs[4].Text != "50" {
				t.Fatal(msgs, err)
			}

			// edits
			db.AddEdit(chat, &Edit{AdminID: 1, Request: "a"})
			db.AddEdit(chat, &Edit{AdminID: 1, Request: "b"})
			es, err := db.RecentEdits(chat, 10)
			if err != nil || len(es) != 2 || es[0].Request != "b" {
				t.Fatal(es, err)
			}
		})
	}
}

func TestMessageTTL(t *testing.T) {
	db, err := Open(t.TempDir(), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.AddMessage(-1, 1, &Message{Text: "bye"})
	time.Sleep(1500 * time.Millisecond)
	if msgs, _ := db.RecentMessages(-1, 10); len(msgs) != 0 {
		t.Fatal("message did not expire", msgs)
	}
}

func TestReopen(t *testing.T) {
	dir := t.TempDir()
	db, err := Open(dir, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	db.UpdateMember(-1, 1, func(m *Member) { m.Username = "x" })
	db.Close()
	db, err = Open(dir, time.Hour) // migrations must accept the existing data
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if m, err := db.MemberByUsername(-1, "x"); err != nil || m.UserID() != 1 {
		t.Fatal(m, err)
	}
}

func TestGroupEventsAndMessageUpdates(t *testing.T) {
	for name, db := range engines(t) {
		t.Run(name, func(t *testing.T) {
			const chat = int64(-555)
			now := time.Now()
			db.AddMessage(chat, 1, &Message{Text: "a", Date: now})
			if err := db.UpdateMessage(chat, 1, func(m *Message) { m.Reactions += 3; m.Replies++ }); err != nil {
				t.Fatal(err)
			}
			if err := db.UpdateMessage(chat, 99, func(m *Message) { t.Fatal("called for a missing message") }); err != nil {
				t.Fatal(err)
			}
			if m, _ := db.GetMessage(chat, 1); m.Engagement() != 7 {
				t.Fatalf("engagement = %d", m.Engagement())
			}

			week := 7 * 24 * time.Hour
			evs := []GroupEvent{
				{Text: "old", FirstAt: now.Add(-10 * 24 * time.Hour), LastAt: now.Add(-8 * 24 * time.Hour)},
				{Text: "kept", Score: 5, FirstAt: now.Add(-time.Hour), LastAt: now},
				{Text: "same instant", FirstAt: now.Add(-time.Hour), LastAt: now},
			}
			if err := db.ReplaceGroupEvents(chat, evs, week); err != nil {
				t.Fatal(err)
			}
			got, err := db.GroupEvents(chat, week)
			if err != nil || len(got) != 2 || got[0].ID == 0 || got[0].ID == got[1].ID {
				t.Fatalf("events = %+v %v", got, err)
			}
			// Replacing with one of them deletes the other.
			if err := db.ReplaceGroupEvents(chat, got[:1], week); err != nil {
				t.Fatal(err)
			}
			if got2, _ := db.GroupEvents(chat, week); len(got2) != 1 || got2[0].ID != got[0].ID {
				t.Fatalf("after replace = %+v", got2)
			}
			if other, _ := db.GroupEvents(chat-1, week); len(other) != 0 {
				t.Fatal("events leaked to another chat")
			}
		})
	}
}
