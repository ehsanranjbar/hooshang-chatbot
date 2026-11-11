package bot

import (
	"testing"
	"time"

	"github.com/mymmrac/telego"

	"github.com/ehsanranjbar/hooshang/internal/i18n"
)

const (
	ownerID = int64(1)
	otherID = int64(2)
)

// groupMsg builds a fresh group message from the given user.
func groupMsg(from *telego.User, text string) *telego.Message {
	return &telego.Message{
		MessageID: 1,
		Chat:      telego.Chat{ID: -100, Type: "supergroup", Title: "g"},
		From:      from,
		Text:      text,
		Date:      time.Now().Unix(),
	}
}

// runCommand drives handleMessage for a group message whose sender is in the
// group (owner present, so access is allowed).
func runCommand(tg *fakeTG, from *telego.User, text string) {
	b, _ := newTestBot(tg, testBotID, testBotUser)
	b.deps.Cfg.Telegram.AdminIDs = []int64{ownerID}
	b.deps.Admins = &ownerAdmins{ownerID}
	tg.member = &telego.ChatMemberOwner{Status: "creator", User: telego.User{ID: ownerID}}
	b.handleMessage(nil, groupMsg(from, text))
}

// ownerAdmins implements auth.Admins for a single owner id.
type ownerAdmins struct{ id int64 }

func (o ownerAdmins) IsAdmin(_, userID int64) bool { return userID == o.id }

func TestHelpCommand(t *testing.T) {
	tg := newFakeTG()
	runCommand(tg, &telego.User{ID: otherID, FirstName: "Ali"}, "/help")
	texts := tg.sentTexts()
	if len(texts) != 1 {
		t.Fatalf("expected 1 message, got %d", len(texts))
	}
	if texts[0] != i18n.Help {
		t.Fatalf("help text mismatch:\n%q", texts[0])
	}
}

func TestNoteaseCommand(t *testing.T) {
	tg := newFakeTG()
	b, db := newTestBot(tg, testBotID, testBotUser)
	b.deps.Cfg.Telegram.AdminIDs = []int64{ownerID}
	b.deps.Admins = &ownerAdmins{ownerID}
	tg.member = &telego.ChatMemberOwner{Status: "creator", User: telego.User{ID: ownerID}}

	b.handleMessage(nil, groupMsg(&telego.User{ID: otherID, FirstName: "Ali"}, "/notease"))
	texts := tg.sentTexts()
	if len(texts) != 1 || texts[0] != i18n.NoTeaseOn {
		t.Fatalf("expected NoTeaseOn, got %v", texts)
	}
	mem, err := db.GetMember(-100, otherID)
	if err != nil || !mem.NoTease {
		t.Fatalf("NoTease should be set, got mem=%v err=%v", mem, err)
	}

	tg = newFakeTG()
	b, _ = newTestBot(tg, testBotID, testBotUser)
	b.deps.Cfg.Telegram.AdminIDs = []int64{ownerID}
	b.deps.Admins = &ownerAdmins{ownerID}
	tg.member = &telego.ChatMemberOwner{Status: "creator", User: telego.User{ID: ownerID}}
	b.handleMessage(nil, groupMsg(&telego.User{ID: otherID, FirstName: "Ali"}, "/teaseok"))
	texts = tg.sentTexts()
	if len(texts) != 1 || texts[0] != i18n.NoTeaseOff {
		t.Fatalf("expected NoTeaseOff, got %v", texts)
	}
}

func TestMeEmpty(t *testing.T) {
	tg := newFakeTG()
	runCommand(tg, &telego.User{ID: otherID, FirstName: "Ali"}, "/me")
	texts := tg.sentTexts()
	if len(texts) != 1 || texts[0] != i18n.NothingYet {
		t.Fatalf("expected NothingYet, got %v", texts)
	}
}

func TestAdminCommandFromNonAdminSilent(t *testing.T) {
	tg := newFakeTG()
	runCommand(tg, &telego.User{ID: otherID, FirstName: "Ali"}, "/status")
	if n := tg.sentCount(); n != 0 {
		t.Fatalf("non-admin /status should be silent, got %d messages", n)
	}
}

func TestAdminStatusCommand(t *testing.T) {
	tg := newFakeTG()
	runCommand(tg, &telego.User{ID: ownerID, FirstName: "Owner"}, "/status")
	if n := tg.sentCount(); n != 1 {
		t.Fatalf("admin /status should send 1 message, got %d", n)
	}
}

// TestIgnoredGroupWithoutOwner: a group where the owner is not a member should
// be denied: the bot sends NotAllowedHere once and leaves, and does not answer
// /help.
func TestIgnoredGroupWithoutOwner(t *testing.T) {
	tg := newFakeTG()
	tg.member = &telego.ChatMemberLeft{Status: "left"}
	b, _ := newTestBot(tg, testBotID, testBotUser)
	b.deps.Cfg.Telegram.AdminIDs = []int64{ownerID}
	b.deps.Admins = &ownerAdmins{ownerID}

	b.handleMessage(nil, groupMsg(&telego.User{ID: otherID, FirstName: "Ali"}, "/help"))

	texts := tg.sentTexts()
	if len(texts) != 1 || texts[0] != i18n.NotAllowedHere {
		t.Fatalf("expected NotAllowedHere, got %v", texts)
	}
	if n := tg.leaveCount(); n != 1 {
		t.Fatalf("expected the bot to leave once, got %d", n)
	}
}

// TestAllowedGroupWithOwnerMember: an owner with "member" status is allowed.
func TestAllowedGroupWithOwnerMember(t *testing.T) {
	tg := newFakeTG()
	tg.member = &telego.ChatMemberMember{Status: "member"}
	b, _ := newTestBot(tg, testBotID, testBotUser)
	b.deps.Cfg.Telegram.AdminIDs = []int64{ownerID}
	b.deps.Admins = &ownerAdmins{ownerID}

	b.handleMessage(nil, groupMsg(&telego.User{ID: otherID, FirstName: "Ali"}, "/help"))
	if n := tg.leaveCount(); n != 0 {
		t.Fatalf("owner is a member: bot should not leave, got %d", n)
	}
	if len(tg.sentTexts()) != 1 {
		t.Fatalf("expected help reply, got %d", tg.sentCount())
	}
}
