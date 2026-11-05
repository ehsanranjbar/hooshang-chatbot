package bot

import (
	"github.com/mymmrac/telego"

	"github.com/ehsanranjbar/hooshang/internal/i18n"
	"github.com/ehsanranjbar/hooshang/internal/store"
)

// isIgnored reports whether the member asked, with /ignoreme, not to be seen.
func (b *Bot) isIgnored(chatID, userID int64) bool {
	mem, err := b.deps.DB.GetMember(chatID, userID)
	return err == nil && mem != nil && mem.Ignored
}

// withoutIgnored returns ids without the members who are ignored.
func (b *Bot) withoutIgnored(chatID int64, ids []int64) []int64 {
	out := ids[:0:0]
	for _, id := range ids {
		if !b.isIgnored(chatID, id) {
			out = append(out, id)
		}
	}
	return out
}

// dropIgnoredSender is the privacy gate of handleMessage: a message from an
// ignored member is dropped before anything is stored, counted, described,
// transcribed or answered. The one exception is /unignoreme, which is
// handled right here (and also not stored). It reports whether the message
// was consumed.
func (b *Bot) dropIgnoredSender(m *telego.Message, stale bool) bool {
	if !b.isIgnored(m.Chat.ID, m.From.ID) {
		return false
	}
	if name, _ := b.splitCommand(m.Text); name == "unignoreme" && !stale {
		b.handleUnignoreMe(m)
	}
	return true
}

// handleIgnoreMe implements /ignoreme: from now on the member's messages are
// not read, and everything the bot kept of them is deleted. The member record
// stays as a minimal {id, first name, ignored} so the flag survives.
func (b *Bot) handleIgnoreMe(m *telego.Message) {
	chatID, userID := m.Chat.ID, m.From.ID
	_, err := b.deps.DB.UpdateMember(chatID, userID, func(x *store.Member) {
		*x = store.Member{ID: x.ID, FirstName: x.FirstName, Ignored: true}
	})
	if err != nil {
		b.logger.Error("ignoreme: member not updated", "chat", chatID, "user", userID, "err", err)
		return
	}
	n, err := b.deps.DB.DeleteMessagesBy(chatID, userID)
	if err != nil {
		b.logger.Error("ignoreme: messages not deleted", "chat", chatID, "user", userID, "err", err)
	}
	// NOTE: when the archive and the vector index exist, purge this member's
	// entries from them here too.
	b.logger.Info("member ignored", "chat", chatID, "user", userID, "messages_deleted", n)
	b.replyCommand(m, i18n.IgnoreOn)
}

// handleUnignoreMe implements /unignoreme: the member is seen again (what was
// deleted stays deleted).
func (b *Bot) handleUnignoreMe(m *telego.Message) {
	_, err := b.deps.DB.UpdateMember(m.Chat.ID, m.From.ID, func(x *store.Member) { x.Ignored = false })
	if err != nil {
		b.logger.Error("unignoreme: member not updated", "chat", m.Chat.ID, "user", m.From.ID, "err", err)
		return
	}
	b.replyCommand(m, i18n.IgnoreOff)
}
