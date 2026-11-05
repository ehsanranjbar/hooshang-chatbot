package bot

import (
	"context"
	"strings"
	"time"

	"github.com/mymmrac/telego"

	"github.com/ehsanranjbar/hooshang/internal/i18n"
	"github.com/ehsanranjbar/hooshang/internal/store"
	"github.com/ehsanranjbar/hooshang/internal/text"
)

// greetingKind says whether a message is a morning or night greeting
// ("greet_morning", "greet_night") or neither ("").
func greetingKind(s string) string {
	f := " " + text.Fold(s) + " "
	for _, w := range i18n.NightGreetings {
		if strings.Contains(f, text.Fold(w)) {
			return "greet_night"
		}
	}
	for _, w := range i18n.MorningGreetings {
		if strings.Contains(f, text.Fold(w)) {
			return "greet_morning"
		}
	}
	switch {
	case strings.Contains(f, "good night"), strings.Contains(f, " gn "):
		return "greet_night"
	case strings.Contains(f, "good morning"):
		return "greet_morning"
	}
	return ""
}

// maybeGreet answers a member's صبح/شب بخیر now and then: when the feature is
// on, outside quiet hours, past the chat and member cooldowns, and the roll is
// under the chance. It reports whether a greeting job was queued.
func (b *Bot) maybeGreet(c store.Chat, m *telego.Message, text string) bool {
	g := b.deps.Cfg.Features.Greetings
	kind := greetingKind(text)
	if !g.On() || kind == "" || c.Muted || b.inQuietHours(time.Now()) {
		return false
	}
	now := time.Now()
	if !c.LastGreetingAt.IsZero() && now.Sub(c.LastGreetingAt) < g.ChatCooldown {
		return false
	}
	mem, err := b.deps.DB.GetMember(m.Chat.ID, m.From.ID)
	if err == nil && !mem.LastGreetedAt.IsZero() && now.Sub(mem.LastGreetedAt) < g.MemberCooldown {
		return false
	}
	if b.roll() >= g.Chance {
		return false
	}
	_ = b.deps.DB.UpdateChat(m.Chat.ID, func(c *store.Chat) { c.LastGreetingAt = now })
	_, _ = b.deps.DB.UpdateMember(m.Chat.ID, m.From.ID, func(mem *store.Member) { mem.LastGreetedAt = now })
	b.enqueueSpontaneous(job{kind: jobSpontaneous, chatID: m.Chat.ID, mode: kind, target: m.From.ID, targetName: displayNameOf(m)})
	return true
}

// handleMembership handles the join/leave service messages: new members are
// stored and welcomed, leavers get a remark. The bot's own join is handled by
// handleMyChatMember; other bots are ignored.
func (b *Bot) handleMembership(ctx context.Context, m *telego.Message) {
	g := b.deps.Cfg.Features.Greetings
	c, _ := b.deps.DB.GetChat(m.Chat.ID)
	for _, u := range m.NewChatMembers {
		if u.IsBot {
			continue
		}
		_, _ = b.deps.DB.UpdateMember(m.Chat.ID, u.ID, func(mem *store.Member) {
			mem.FirstName, mem.LastName, mem.Username = u.FirstName, u.LastName, u.Username
			mem.LastSeen = time.Unix(m.Date, 0)
		})
		if g.On() && g.Welcome && !c.Muted {
			b.enqueueEvent(job{kind: jobSpontaneous, chatID: m.Chat.ID, mode: "welcome", target: u.ID, targetName: userName(u)})
		}
	}
	if u := m.LeftChatMember; u != nil && !u.IsBot && g.On() && g.Farewell && !c.Muted && !b.inQuietHours(time.Now()) {
		b.enqueueEvent(job{kind: jobSpontaneous, chatID: m.Chat.ID, mode: "farewell", target: u.ID, targetName: userName(*u)})
	}
}

// enqueueEvent queues a spontaneous job that must not be dropped for a busy
// worker (a welcome), so it goes in like a reply.
func (b *Bot) enqueueEvent(j job) {
	if !b.workers.enqueue(j, false) {
		b.logger.Info("event dropped: chat queue full", "chat", j.chatID, "mode", j.mode)
	}
}

// userName renders a Telegram user's display name.
func userName(u telego.User) string {
	if u.LastName != "" {
		return u.FirstName + " " + u.LastName
	}
	return u.FirstName
}
