package bot

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/mymmrac/telego"

	"github.com/ehsanranjbar/hooshang/internal/i18n"
	"github.com/ehsanranjbar/hooshang/internal/memory"
	"github.com/ehsanranjbar/hooshang/internal/store"
)

// handleCommand dispatches a message whose text starts with "/" to the right
// command. The command name is the first token; any "@botusername" suffix is
// stripped. Admin commands from a non-admin are silently ignored.
func (b *Bot) handleCommand(ctx context.Context, m *telego.Message, text string) {
	name, args := b.splitCommand(text)
	chatID := m.Chat.ID
	from := m.From.ID
	isAdmin := b.deps.Admins.IsAdmin(chatID, from)

	switch name {
	case "start", "help":
		b.replyCommand(m, i18n.Help)

	case "faal":
		wish := strings.TrimSpace(args)
		b.enqueueFunc(chatID, func(ctx context.Context) { b.handleFaal(ctx, m, wish) })

	case "poem":
		poet := strings.TrimSpace(args)
		b.enqueueFunc(chatID, func(ctx context.Context) { b.handlePoem(ctx, m, poet) })

	case "roast":
		b.handleRoast(ctx, m)

	case "me":
		b.handleMe(m)

	case "terms":
		b.handleTerms(m)

	case "notease":
		_, _ = b.deps.DB.UpdateMember(chatID, from, func(x *store.Member) { x.NoTease = true })
		b.replyCommand(m, i18n.NoTeaseOn)

	case "teaseok":
		_, _ = b.deps.DB.UpdateMember(chatID, from, func(x *store.Member) { x.NoTease = false })
		b.replyCommand(m, i18n.NoTeaseOff)

	case "forgetme":
		_ = b.deps.DB.DeleteMember(chatID, from)
		b.replyCommand(m, i18n.Forgotten)

	case "mute":
		if !isAdmin {
			return
		}
		_ = b.deps.DB.UpdateChat(chatID, func(c *store.Chat) { c.Muted = true })
		b.replyCommand(m, i18n.Muted)

	case "unmute":
		if !isAdmin {
			return
		}
		_ = b.deps.DB.UpdateChat(chatID, func(c *store.Chat) { c.Muted = false })
		b.replyCommand(m, i18n.Unmuted)

	case "status":
		if !isAdmin {
			return
		}
		b.handleStatus(m)

	default:
		// Unknown commands are ignored (not our commands).
	}
}

// splitCommand returns the command name (without "/" and without any "@bot"
// suffix) and the remaining argument text.
func (b *Bot) splitCommand(text string) (name, args string) {
	text = strings.TrimSpace(text)
	if !strings.HasPrefix(text, "/") {
		return "", text
	}
	body := text[1:]
	sp := strings.IndexAny(body, " \t")
	if sp < 0 {
		return strings.TrimSuffix(body, "@"+b.botUsername), ""
	}
	name = body[:sp]
	args = strings.TrimSpace(body[sp+1:])
	name = strings.TrimSuffix(name, "@"+b.botUsername)
	return name, args
}

// replyCommand sends a command reply as a reply to m, reserving one limiter
// slot first. If the reservation fails it does nothing.
func (b *Bot) replyCommand(m *telego.Message, text string) {
	if text == "" {
		return
	}
	if !b.deps.Limiter.Reserve(m.Chat.ID, 1) {
		b.logger.Debug("command rate limited", "chat", m.Chat.ID)
		return
	}
	_ = b.send(context.Background(), m.Chat.ID, m.MessageID, text)
}

// handleMe renders the sender's own profile, or NothingYet.
func (b *Bot) handleMe(m *telego.Message) {
	mem, err := b.deps.DB.GetMember(m.Chat.ID, m.From.ID)
	if err != nil || mem == nil {
		b.replyCommand(m, i18n.NothingYet)
		return
	}
	r := memory.Render(mem.Profile)
	if r == "" {
		b.replyCommand(m, i18n.NothingYet)
		return
	}
	b.replyCommand(m, r)
}

// handleTerms lists the group's glossary, most recently used first.
func (b *Bot) handleTerms(m *telego.Message) {
	terms, _ := b.deps.DB.Terms(m.Chat.ID)
	if !b.deps.Cfg.Glossary.Enabled || len(terms) == 0 {
		b.replyCommand(m, i18n.NoTerms)
		return
	}
	var sb strings.Builder
	sb.WriteString(i18n.TermsHeader)
	for _, t := range terms {
		line := fmt.Sprintf("\n• %s: %s", t.Term, t.Meaning)
		if sb.Len()+len(line) > maxChunk-16 {
			sb.WriteString("\n…")
			break
		}
		sb.WriteString(line)
	}
	b.replyCommand(m, sb.String())
}

// handleStatus reports the bot's status for the admin.
func (b *Bot) handleStatus(m *telego.Message) {
	chatID := m.Chat.ID
	_, chatFree := b.deps.Limiter.Free(chatID)
	workers := b.workers.Len()
	queue := 0
	if b.deps.Memory != nil {
		queue = b.deps.Memory.QueueLen()
	}
	members, _ := b.deps.DB.Members(chatID)
	edits, _ := b.deps.DB.RecentEdits(chatID, 3)

	var sb strings.Builder
	fmt.Fprintf(&sb, "📊 %s\n", b.deps.Cfg.Bot.Name)
	fmt.Fprintf(&sb, "Model: %s\n", b.deps.Cfg.LLM.Model)
	fmt.Fprintf(&sb, "Limiter (chat): %d free / min\n", chatFree)
	fmt.Fprintf(&sb, "Workers: %d\n", workers)
	fmt.Fprintf(&sb, "Memory queue: %d\n", queue)
	fmt.Fprintf(&sb, "Members: %d\n", len(members))
	if g := b.deps.Cfg.GroupMemory; g.Enabled {
		events, _ := b.deps.DB.GroupEvents(chatID, g.TTL)
		fmt.Fprintf(&sb, "Group memory: %d events\n", len(events))
	}
	if g := b.deps.Cfg.Glossary; g.Enabled {
		terms, _ := b.deps.DB.Terms(chatID)
		fmt.Fprintf(&sb, "Glossary: %d/%d terms\n", len(terms), g.MaxItems)
	}
	if len(edits) > 0 {
		sb.WriteString("\nLast edits:\n")
		for _, e := range edits {
			fmt.Fprintf(&sb, "• %s (user %s)\n", e.Request, strconv.FormatInt(e.UserID, 10))
		}
	}
	b.replyCommand(m, sb.String())
}
