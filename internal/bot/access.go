package bot

import (
	"context"
	"time"

	"github.com/mymmrac/telego"

	"github.com/ehsanranjbar/hooshang/internal/i18n"
)

// allowed reports whether the bot may act in this chat for this user. It
// implements the access control in §5.
func (b *Bot) allowed(ctx context.Context, chat telego.Chat, from *telego.User) bool {
	switch chat.Type {
	case "private":
		if b.deps.Admins.IsAdmin(chat.ID, from.ID) {
			return true
		}
		// This is how the owner finds their own ID.
		b.logger.Info("ignored private message", "user_id", from.ID)
		return false

	case "group", "supergroup":
		return b.ownerInGroup(ctx, chat.ID)

	default:
		// Channels and every other chat type are ignored.
		return false
	}
}

// allowedTTL and deniedTTL are the per-chat cache durations.
const (
	allowedTTL = time.Hour
	deniedTTL  = 5 * time.Minute
	errorTTL   = 30 * time.Second // after a Telegram API error: re-check soon
)

// accessEntry is one cached group-access verdict.
type accessEntry struct {
	allowed bool
	expiry  time.Time
}

// ownerInGroup checks that the owner is a member of the group. The verdict is
// cached per chat for 1h (allowed) or 5m (denied). On a real denial it sends
// NotAllowedHere once and leaves the chat. On an API error it denies for
// errorTTL only and does NOT leave, so the group isn't locked out by a blip.
func (b *Bot) ownerInGroup(ctx context.Context, chatID int64) bool {
	b.accessMu.Lock()
	if entry, ok := b.accessCache[chatID]; ok && time.Now().Before(entry.expiry) {
		b.accessMu.Unlock()
		return entry.allowed
	}
	b.accessMu.Unlock()

	allowed, leave := b.checkOwner(ctx, chatID)
	ttl := ttlFor(allowed)
	if !allowed && !leave {
		ttl = errorTTL
	}
	b.accessMu.Lock()
	b.accessCache[chatID] = accessEntry{allowed: allowed, expiry: time.Now().Add(ttl)}
	b.accessMu.Unlock()

	if !allowed && leave {
		_ = b.send(ctx, chatID, 0, i18n.NotAllowedHere)
		_ = b.deps.TG.LeaveChat(ctx, &telego.LeaveChatParams{ChatID: newChatID(chatID)})
	}
	return allowed
}

// ttlFor returns the cache TTL for a verdict.
func ttlFor(allowed bool) time.Duration {
	if allowed {
		return allowedTTL
	}
	return deniedTTL
}

// checkOwner calls GetChatMember for the owner and applies the §5 rules.
// It returns whether the owner is accepted and whether a real denial occurred
// (an API error denies but reports leave=false so the bot does not leave).
func (b *Bot) checkOwner(ctx context.Context, chatID int64) (allowed, leave bool) {
	cm, err := b.deps.TG.GetChatMember(ctx, &telego.GetChatMemberParams{
		ChatID: newChatID(chatID),
		UserID: b.deps.Cfg.Telegram.OwnerID,
	})
	if err != nil {
		b.logger.Debug("getChatMember failed, denying without leaving", "chat", chatID, "err", err)
		return false, false
	}
	return memberAllowed(cm), true
}

// memberAllowed applies the §5 status rules to a chat member.
func memberAllowed(cm telego.ChatMember) bool {
	switch cm.MemberStatus() {
	case "creator", "administrator", "member":
		return true
	case "restricted":
		return cm.MemberIsMember()
	default:
		return cm.MemberIsMember()
	}
}

// handleMyChatMember implements the §5 my_chat_member rules.
func (b *Bot) handleMyChatMember(ctx context.Context, u *telego.ChatMemberUpdated) {
	chat := u.Chat
	if chat.Type != "group" && chat.Type != "supergroup" {
		return
	}
	switch u.NewChatMember.MemberStatus() {
	case "member", "administrator", "creator":
		if b.deps.Admins.IsAdmin(chat.ID, u.From.ID) {
			// The admin added it: cache the chat as allowed.
			b.accessMu.Lock()
			b.accessCache[chat.ID] = accessEntry{allowed: true, expiry: time.Now().Add(allowedTTL)}
			b.accessMu.Unlock()
			return
		}
		// A non-admin added the bot: leave immediately.
		b.logger.Info("bot added by non-admin, leaving", "chat", chat.ID)
		_ = b.deps.TG.LeaveChat(ctx, &telego.LeaveChatParams{ChatID: newChatID(chat.ID)})
	}
}
