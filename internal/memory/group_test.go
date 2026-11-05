package memory

import (
	"context"
	"github.com/ehsanranjbar/hooshang/internal/i18n"
	"strings"
	"testing"
	"time"

	"github.com/ehsanranjbar/hooshang/internal/llm"
	"github.com/ehsanranjbar/hooshang/internal/store"
)

const groupChat = int64(-77)

var week = 7 * 24 * time.Hour

func groupUpdater(db *store.DB, fake *llm.Fake) *Updater {
	return NewUpdater(db, fake, "m", UpdaterConfig{
		ContextMessages: 150, MaxItems: 10, Language: "fa",
		Group: GroupConfig{TTL: week, MaxItems: 3},
	}, nil)
}

// seedGroup stores n messages; message 5 got 4 reactions and 2 replies.
func seedGroup(t *testing.T, db *store.DB, n int) {
	t.Helper()
	for i := 1; i <= n; i++ {
		if err := db.AddMessage(groupChat, int64(i), &store.Message{UserID: int64(i%3 + 1), Name: "user", Text: "پیام", Date: time.Now()}); err != nil {
			t.Fatal(err)
		}
	}
	_ = db.UpdateMessage(groupChat, 5, func(m *store.Message) { m.Reactions, m.Replies = 4, 2 })
}

func TestGroupScoresComeFromEngagement(t *testing.T) {
	db := testDB(t)
	seedGroup(t, db, 12)
	fake := &llm.Fake{}
	fake.Queue(llm.Response{Content: "```json\n" + `{"events":[
		{"text":"سارا کارش رو عوض کرد","msgs":[5,6],"same_as":0},
		{"text":"بحث سر شام","msgs":[999],"same_as":0}]}` + "\n```"})
	u := groupUpdater(db, fake)
	if err := u.UpdateGroupNow(context.Background(), groupChat); err != nil {
		t.Fatal(err)
	}
	evs, _ := db.GroupEvents(groupChat, week)
	if len(evs) != 2 {
		t.Fatalf("events = %+v", evs)
	}
	scores := map[string]int{}
	for _, e := range evs {
		scores[e.Text] = e.Score
	}
	// 1 + engagement(5)=2*4+2 + engagement(6)=0 → 11; a made-up id counts 0 → 1.
	if scores["سارا کارش رو عوض کرد"] != 11 || scores["بحث سر شام"] != 1 {
		t.Fatalf("scores = %v", scores)
	}
	if !strings.Contains(fake.Calls[0].Messages[0].Content, "[5] user: پیام {+10}") {
		t.Errorf("engagement is not shown to the model:\n%s", fake.Calls[0].Messages[0].Content)
	}
	if c, _ := db.GetChat(groupChat); c.GroupUpdatedAt.IsZero() || c.MsgsSinceGroupUpdate != 0 {
		t.Fatalf("chat = %+v", c)
	}
}

func TestGroupSameAsUpdatesAndCapKeepsTheTop(t *testing.T) {
	db := testDB(t)
	now := time.Now()
	_ = db.ReplaceGroupEvents(groupChat, []store.GroupEvent{
		{Text: "برنامه کوه جمعه", Score: 3, FirstAt: now.Add(-48 * time.Hour), LastAt: now.Add(-48 * time.Hour)},
		{Text: "کم‌اهمیت", Score: 1, FirstAt: now.Add(-time.Hour), LastAt: now.Add(-time.Hour)},
	}, week)
	evs, _ := db.GroupEvents(groupChat, week)
	var kooh int64
	for _, e := range evs {
		if e.Score == 3 {
			kooh = e.ID
		}
	}
	seedGroup(t, db, 12)
	fake := &llm.Fake{}
	fake.Queue(llm.Response{Content: `{"events":[
		{"text":"برنامه کوه جمعه قطعی شد","msgs":[5],"same_as":` + itoa(kooh) + `},
		{"text":"الف","msgs":[1],"same_as":0},
		{"text":"ب","msgs":[2],"same_as":0}]}`})
	if err := groupUpdater(db, fake).UpdateGroupNow(context.Background(), groupChat); err != nil {
		t.Fatal(err)
	}
	evs, _ = db.GroupEvents(groupChat, week)
	if len(evs) != 3 {
		t.Fatalf("cap 3: %+v", evs)
	}
	for _, e := range evs {
		if e.ID == kooh && (e.Text != "برنامه کوه جمعه قطعی شد" || e.Score != 3+11 || time.Since(e.LastAt) > time.Minute) {
			t.Fatalf("same_as not merged: %+v", e)
		}
		if e.Text == "کم‌اهمیت" {
			// score 1 and older than the new score-2 events: it is the one dropped
			t.Fatalf("the lowest event survived the cap: %+v", evs)
		}
	}
}

func TestGroupBadOutputKeepsEvents(t *testing.T) {
	db := testDB(t)
	now := time.Now()
	_ = db.ReplaceGroupEvents(groupChat, []store.GroupEvent{{Text: "x", Score: 2, FirstAt: now, LastAt: now}}, week)
	seedGroup(t, db, 12)
	for _, out := range []string{"sorry", `{"2+2":4}`} {
		fake := &llm.Fake{}
		fake.Queue(llm.Response{Content: out})
		if err := groupUpdater(db, fake).UpdateGroupNow(context.Background(), groupChat); err != nil {
			t.Fatal(err)
		}
		if evs, _ := db.GroupEvents(groupChat, week); len(evs) != 1 || evs[0].Text != "x" {
			t.Fatalf("%q changed the events: %+v", out, evs)
		}
	}
}

func TestGroupNeedsEnoughNewMessages(t *testing.T) {
	db := testDB(t)
	seedGroup(t, db, 5)
	fake := &llm.Fake{}
	if err := groupUpdater(db, fake).UpdateGroupNow(context.Background(), groupChat); err != nil {
		t.Fatal(err)
	}
	if len(fake.Calls) != 0 {
		t.Fatal("called the LLM for 5 messages")
	}
}

func TestRenderGroup(t *testing.T) {
	now := time.Now()
	evs := []store.GroupEvent{
		{Text: "قدیمی و مهم", Score: 9, LastAt: now.Add(-3 * 24 * time.Hour)},
		{Text: "دیروزی", Score: 5, LastAt: now.Add(-30 * time.Hour)},
		{Text: "کم", Score: 1, LastAt: now},
	}
	got := RenderGroup(evs, 2, now)
	want := "- (دیروز) دیروزی\n- (" + i18n.FormatDate(now.Add(-3*24*time.Hour)) + ") قدیمی و مهم"
	if got != want {
		t.Fatalf("got\n%s\nwant\n%s", got, want)
	}
}

func itoa(n int64) string {
	b := []byte{}
	if n == 0 {
		return "0"
	}
	for ; n > 0; n /= 10 {
		b = append([]byte{byte('0' + n%10)}, b...)
	}
	return string(b)
}
