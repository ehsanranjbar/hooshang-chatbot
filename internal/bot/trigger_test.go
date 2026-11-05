package bot

import (
	"testing"
	"time"

	"github.com/mymmrac/telego"

	"github.com/ehsanranjbar/hooshang/internal/store"
)

const (
	testBotID   = int64(900000001)
	testBotUser = "hooshang_bot"
)

func msgWith(text string) *telego.Message {
	return &telego.Message{
		MessageID: 1,
		Chat:      telego.Chat{ID: -100, Type: "supergroup"},
		From:      &telego.User{ID: 42, FirstName: "Ali"},
		Text:      text,
		Date:      time.Now().Unix(),
	}
}

func TestIsAddressedPrivate(t *testing.T) {
	if !IsAddressed(msgWith("anything"), testBotID, testBotUser, []string{"هوشنگ"}, true) {
		t.Fatal("private chat should always be addressed")
	}
}

func TestIsAddressedReplyToBot(t *testing.T) {
	m := msgWith("tell me a joke")
	m.ReplyToMessage = &telego.Message{From: &telego.User{ID: testBotID}}
	if !IsAddressed(m, testBotID, testBotUser, []string{"هوشنگ"}, false) {
		t.Fatal("reply to the bot's own message should be addressed")
	}
	// Reply to someone else is not addressed.
	m.ReplyToMessage = &telego.Message{From: &telego.User{ID: 7}}
	if IsAddressed(m, testBotID, testBotUser, []string{"هوشنگ"}, false) {
		t.Fatal("reply to a non-bot message should not be addressed")
	}
}

func TestIsAddressedMention(t *testing.T) {
	m := msgWith("hey @hooshang_bot hi")
	m.Entities = []telego.MessageEntity{{
		Type:   "mention",
		Offset: 4,
		Length: len("@hooshang_bot"),
	}}
	if !IsAddressed(m, testBotID, testBotUser, []string{"هوشنگ"}, false) {
		t.Fatal("@username mention should be addressed")
	}
}

func TestIsAddressedTextMention(t *testing.T) {
	m := msgWith("hello there")
	m.Entities = []telego.MessageEntity{{
		Type:   "text_mention",
		Offset: 0,
		Length: 5,
		User:   &telego.User{ID: testBotID},
	}}
	if !IsAddressed(m, testBotID, testBotUser, []string{"هوشنگ"}, false) {
		t.Fatal("text_mention of the bot should be addressed")
	}
}

func TestIsAddressedNameBoundaries(t *testing.T) {
	names := []string{"هوشنگ", "hooshang"}
	cases := []struct {
		text string
		want bool
	}{
		{"سلام هوشنگ", true},    // exact word
		{"هوشنگی چی؟", false},   // "هوشنگ" prefix of a longer word: no
		{"هوشنگ، سلام", true},   // followed by punctuation: yes
		{"من هوشنگ هستم", true}, // space neighbors: yes
		{"hooshang!", true},     // alias with punctuation
		{"hooshangi", false},    // longer word: no
		{"سلام", false},
	}
	for _, c := range cases {
		if got := IsAddressed(msgWith(c.text), testBotID, testBotUser, names, false); got != c.want {
			t.Errorf("IsAddressed(%q) = %v, want %v", c.text, got, c.want)
		}
	}
}

func TestShouldSpontaneous(t *testing.T) {
	cfg := SpontaneousConfig{Chance: 0.5, Cooldown: 5 * time.Minute, MinMsgsBetween: 8}
	now := time.Now()
	base := store.Chat{}

	if ShouldSpontaneous(base, now, cfg, 0.1) {
		t.Fatal("no messages yet: should not be spontaneous")
	}
	c := store.Chat{MsgsSinceBot: 8}
	if !ShouldSpontaneous(c, now, cfg, 0.1) {
		t.Fatal("enough messages, no bot recently: should be spontaneous")
	}
	if ShouldSpontaneous(c, now, cfg, 0.9) {
		t.Fatal("roll above chance: should not be spontaneous")
	}
	// Muted.
	c2 := store.Chat{MsgsSinceBot: 8, Muted: true}
	if ShouldSpontaneous(c2, now, cfg, 0.1) {
		t.Fatal("muted chat: should not be spontaneous")
	}
	// Cooldown not elapsed.
	c3 := store.Chat{MsgsSinceBot: 8, LastBotAt: now.Add(-1 * time.Minute)}
	if ShouldSpontaneous(c3, now, cfg, 0.1) {
		t.Fatal("cooldown not elapsed: should not be spontaneous")
	}
	// Cooldown elapsed.
	c4 := store.Chat{MsgsSinceBot: 8, LastBotAt: now.Add(-10 * time.Minute)}
	if !ShouldSpontaneous(c4, now, cfg, 0.1) {
		t.Fatal("cooldown elapsed: should be spontaneous")
	}
}

func TestPickMode(t *testing.T) {
	w := map[string]int{"joke": 3, "tease": 3, "flirt": 1, "compliment": 2, "comment": 2}
	// roll 0 → first mode in order with weight (joke).
	if got := PickMode(w, 0); got != "joke" {
		t.Fatalf("PickMode(0) = %q, want joke", got)
	}
	// A very high roll → last mode (comment).
	if got := PickMode(w, 0.999999); got != "comment" {
		t.Fatalf("PickMode(0.999999) = %q, want comment", got)
	}
	// No positive weights → comment.
	if got := PickMode(map[string]int{"joke": 0}, 0.5); got != "comment" {
		t.Fatalf("PickMode(no weights) = %q, want comment", got)
	}
}

func TestPickTarget(t *testing.T) {
	members := map[int64]*store.Member{
		1: {NoTease: false},
		2: {NoTease: true},
		3: {NoTease: false},
	}
	recent := []store.Message{
		{UserID: 1},
		{UserID: 2}, // NoTease → excluded
		{UserID: 1},
		{UserID: testBotID, FromBot: true}, // bot → excluded
		{UserID: 3},
	}
	// Eligible distinct authors (newest first): 3, 1.
	for roll := 0.0; roll < 1.0; roll += 0.25 {
		id, ok := PickTarget(recent, members, testBotID, roll)
		if !ok {
			t.Fatal("PickTarget should find an eligible author")
		}
		if id == testBotID {
			t.Fatalf("PickTarget returned the bot")
		}
		if id == 2 {
			t.Fatalf("PickTarget returned a NoTease member")
		}
	}
}

func TestPickTargetNone(t *testing.T) {
	members := map[int64]*store.Member{1: {NoTease: true}}
	recent := []store.Message{{UserID: 1}}
	if _, ok := PickTarget(recent, members, testBotID, 0.5); ok {
		t.Fatal("all authors are NoTease: PickTarget should return ok=false")
	}
}

func TestEntityTextCountsUTF16(t *testing.T) {
	text := "😂😂 @hooshang_bot بیا"
	// Each 😂 is 2 UTF-16 units: the mention starts at 5, not 3.
	if got := entityText(text, telego.MessageEntity{Offset: 5, Length: 13}); got != "@hooshang_bot" {
		t.Fatalf("entityText = %q", got)
	}
}
