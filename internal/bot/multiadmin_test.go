package bot

import (
	"context"
	"strings"
	"testing"

	"github.com/mymmrac/telego"

	"github.com/ehsanranjbar/hooshang/internal/auth"
	"github.com/ehsanranjbar/hooshang/internal/prompts"
)

// These tests wire Admins the way main.go does, from telegram.admin_ids, and
// check that the second admin on the list gets everything the first one does.

// useAdminList sets admin_ids and builds Admins from it as main.go does.
func useAdminList(b *Bot, ids ...int64) {
	b.deps.Cfg.Telegram.AdminIDs = ids
	b.deps.Admins = auth.List{IDs: b.deps.Cfg.Telegram.AdminIDs}
}

func TestSecondAdminRunsAdminCommands(t *testing.T) {
	tg := newFakeTG()
	tg.member = &telego.ChatMemberOwner{Status: "creator", User: telego.User{ID: ownerID}}
	b, _ := newTestBot(tg, testBotID, testBotUser)
	useAdminList(b, ownerID, sara.ID)

	b.handleMessage(nil, groupMsg(sara, "/status"))
	if n := tg.sentCount(); n != 1 {
		t.Fatalf("second admin /status should send 1 message, got %d", n)
	}
	b.handleMessage(nil, groupMsg(ali, "/status"))
	if n := tg.sentCount(); n != 1 {
		t.Fatalf("non-admin /status should be silent, got %d messages in total", n)
	}
}

func TestGroupAllowedWhenOnlySecondAdminIsMember(t *testing.T) {
	tg := newFakeTG()
	tg.members = map[int64]telego.ChatMember{sara.ID: &telego.ChatMemberMember{Status: "member"}}
	b, _ := newTestBot(tg, testBotID, testBotUser)
	useAdminList(b, ownerID, sara.ID)

	if !b.adminInGroup(context.Background(), testChat) {
		t.Fatal("group with the second admin as a member was denied")
	}
	if len(tg.leaves) != 0 {
		t.Fatal("left a group the second admin is in")
	}

	tg2 := newFakeTG()
	tg2.members = map[int64]telego.ChatMember{}
	b2, _ := newTestBot(tg2, testBotID, testBotUser)
	useAdminList(b2, ownerID, sara.ID)
	if b2.adminInGroup(context.Background(), testChat) || len(tg2.leaves) != 1 {
		t.Fatalf("group with no admin: want denied and left once, leaves=%d", len(tg2.leaves))
	}
}

func TestSecondAdminGetsAdminPromptAndTools(t *testing.T) {
	b, _, _, _ := modBot(t)
	useAdminList(b, ownerID, sara.ID)

	for _, tc := range []struct {
		who   *telego.User
		admin bool
	}{{boss, true}, {sara, true}, {ali, false}} {
		msg := say(b, 40, tc.who, "هوشنگ علی رو بن کن")
		j := &job{kind: jobReply, chatID: testChat, msg: msg}
		req, handlers := b.replyRequest(context.Background(), j)
		rules := strings.TrimSpace(prompts.AdminRules(true, true))
		gotRules := strings.Contains(req.Messages[1].Content, rules) // at the end of the task, not in the system prompt
		_, gotMod := handlers["moderate"]
		_, gotMem := handlers["edit_memory"]
		if gotRules != tc.admin || gotMod != tc.admin || gotMem != tc.admin {
			t.Errorf("%s: admin rules %v, moderate %v, edit_memory %v; want all %v",
				tc.who.FirstName, gotRules, gotMod, gotMem, tc.admin)
		}
	}
}

func TestSecondAdminModeratesButCannotTargetFirst(t *testing.T) {
	b, _, tg, _ := modBot(t)
	useAdminList(b, ownerID, sara.ID)
	say(b, 3, boss, "من اینجام")

	cmd := say(b, 30, sara, "هوشنگ علی رو بن کن")
	if out := modCall(t, b, cmd, `{"action":"ban","member":"@ali"}`); !strings.HasPrefix(out, "done:") || len(tg.bans) != 1 {
		t.Fatalf("second admin ban = %q, bans %d", out, len(tg.bans))
	}
	if out := modCall(t, b, cmd, `{"action":"ban","member":"@boss"}`); !strings.Contains(out, "is an admin") {
		t.Fatalf("second admin banning the first = %q", out)
	}
	byBoss := say(b, 31, boss, "هوشنگ سارا رو ساکت کن")
	if out := modCall(t, b, byBoss, `{"action":"mute","member":"@sara"}`); !strings.Contains(out, "is an admin") {
		t.Fatalf("first admin muting the second = %q", out)
	}
}
