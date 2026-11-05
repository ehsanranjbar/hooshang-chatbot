package bot

import (
	"context"
	"log/slog"
	"math/rand"
	"strings"
	"sync"
	"time"

	"github.com/mymmrac/telego"

	"github.com/ehsanranjbar/hooshang/internal/auth"
	"github.com/ehsanranjbar/hooshang/internal/config"
	"github.com/ehsanranjbar/hooshang/internal/faalimg"
	"github.com/ehsanranjbar/hooshang/internal/ganjoor"
	"github.com/ehsanranjbar/hooshang/internal/llm"
	"github.com/ehsanranjbar/hooshang/internal/memory"
	"github.com/ehsanranjbar/hooshang/internal/ratelimit"
	"github.com/ehsanranjbar/hooshang/internal/store"
)

// Deps are the bot's dependencies.
type Deps struct {
	Cfg     *config.Config
	TG      TG
	DB      *store.DB
	LLM     llm.Client
	Ganjoor *ganjoor.Client
	Limiter *ratelimit.Limiter
	Admins  auth.Admins
	Memory  *memory.Updater
	// FaalImage and PoemImage render the faal ghazal and the /poem بیت as a
	// Nastaliq image (usually the same renderer); nil sends text.
	FaalImage *faalimg.Renderer
	PoemImage *faalimg.Renderer
}

// Bot is the hooshang Telegram bot.
type Bot struct {
	deps        Deps
	logger      *slog.Logger
	botID       int64
	botUsername string
	workers     *workers
	rollFn      func() float64 // nil = math/rand; DumpPrompt pins it
	describer   *describer     // background media descriptions; nil until Run
	accessMu    sync.Mutex
	accessCache map[int64]accessEntry
}

// New creates a Bot. It calls GetMe once and keeps the bot's ID and username.
func New(d Deps, logger *slog.Logger) (*Bot, error) {
	if logger == nil {
		logger = slog.Default()
	}
	b := &Bot{
		deps:        d,
		logger:      logger,
		accessCache: make(map[int64]accessEntry),
	}
	b.workers = newWorkers(b)
	// GetMe needs the concrete *telego.Bot (it is not on the TG interface).
	if tb, ok := d.TG.(*telego.Bot); ok {
		me, err := tb.GetMe(context.Background())
		if err != nil {
			return nil, err
		}
		b.botID = me.ID
		b.botUsername = me.Username
	}
	return b, nil
}

// BotID returns the bot's own Telegram user id.
func (b *Bot) BotID() int64 { return b.botID }

// BotUsername returns the bot's username (without @).
func (b *Bot) BotUsername() string { return b.botUsername }

// Run starts long polling and dispatches updates until ctx is done.
func (b *Bot) Run(ctx context.Context) error {
	tb, ok := b.deps.TG.(*telego.Bot)
	if !ok {
		return errConcreteTG
	}
	updates, err := tb.UpdatesViaLongPolling(ctx, &telego.GetUpdatesParams{
		Timeout: 30,
		// Reactions arrive only while the bot is a group admin.
		AllowedUpdates: []string{"message", "my_chat_member", "message_reaction", "message_reaction_count"},
	})
	if err != nil {
		return err
	}
	dctx, stopDescriber := context.WithCancel(ctx)
	b.describer = newDescriber(b)
	go b.describer.run(dctx)
	shutdown := func() {
		b.workers.stop(workerStopTimeout)
		stopDescriber()
		select { // the DB closes after Run returns
		case <-b.describer.done:
		case <-time.After(workerStopTimeout):
		}
	}
	for {
		select {
		case <-ctx.Done():
			shutdown()
			return nil
		case u, ok := <-updates:
			if !ok {
				shutdown()
				return nil
			}
			b.handleUpdate(ctx, u)
		}
	}
}

// handleUpdate dispatches one update in the §3.9 order.
func (b *Bot) handleUpdate(ctx context.Context, u telego.Update) {
	if u.MyChatMember != nil {
		b.handleMyChatMember(ctx, u.MyChatMember)
		return
	}
	if u.MessageReaction != nil {
		r := u.MessageReaction
		if r.User != nil && r.User.ID == b.botID {
			return // the bot's own reaction is not the group's engagement
		}
		b.handleReaction(ctx, r.Chat, r.MessageID, len(r.NewReaction)-len(r.OldReaction), -1)
		return
	}
	if u.MessageReactionCount != nil {
		r := u.MessageReactionCount
		total := 0
		for _, c := range r.Reactions {
			total += c.TotalCount
		}
		b.handleReaction(ctx, r.Chat, r.MessageID, 0, total)
		return
	}
	if u.Message == nil {
		return
	}
	b.handleMessage(ctx, u.Message)
}

// workerStopTimeout is how long shutdown waits for running jobs.
const workerStopTimeout = 10 * time.Second

// handleReaction records reactions on a stored message: delta for a member's
// change, or total (>= 0) for the anonymous count Telegram reports.
func (b *Bot) handleReaction(ctx context.Context, chat telego.Chat, messageID, delta, total int) {
	if chat.Type != "group" && chat.Type != "supergroup" {
		return
	}
	if !b.ownerInGroup(ctx, chat.ID) {
		return
	}
	_ = b.deps.DB.UpdateMessage(chat.ID, int64(messageID), func(m *store.Message) {
		if total >= 0 {
			m.Reactions = total
		} else {
			m.Reactions = max(0, m.Reactions+delta)
		}
	})
}

// staleWindow is how old a message may be before it is only stored, not
// answered (backlog).
const staleWindow = 2 * time.Minute

// handleMessage implements the §3.9 message-handling order.
func (b *Bot) handleMessage(ctx context.Context, m *telego.Message) {
	// 2. Ignore messages without a sender or from other bots.
	if m.From == nil || m.From.IsBot {
		return
	}
	// Messages older than 2 minutes are the backlog after downtime (Telegram
	// keeps undelivered updates for 24h): they are stored, counted and
	// described like any other, but never answered.
	stale := time.Since(time.Unix(m.Date, 0)) > staleWindow

	chat := m.Chat
	// 3. Access control.
	if !b.allowed(ctx, chat, m.From) {
		return
	}

	// Compute the text (or a media tag); ignore service messages.
	text, isService := messageText(m)
	if isService {
		return
	}

	// 4. Store the message, the member, and the chat counters.
	b.storeIncoming(m, text)
	b.describeIncoming(m)

	// 5. Memory update trigger.
	b.maybeEnqueueMemory(chat.ID, m.From.ID)
	if stale {
		return
	}

	// 6. Commands.
	if strings.HasPrefix(text, "/") {
		b.handleCommand(ctx, m, text)
		return
	}

	// 7. Reply or spontaneous.
	private := chat.Type == "private"
	if IsAddressed(m, b.botID, b.botUsername, b.names(), private) {
		b.enqueueReply(job{kind: jobReply, chatID: chat.ID, msg: m})
		return
	}
	c, _ := b.deps.DB.GetChat(chat.ID)
	if ShouldReact(c, time.Now(), b.deps.Cfg.Bot.ReactionChance, b.deps.Cfg.Bot.ReactionCooldown, b.roll()) {
		b.enqueueSpontaneous(job{kind: jobReaction, chatID: chat.ID, msg: m})
		return // a reaction is the bot's answer to this message
	}
	if ShouldSpontaneous(c, time.Now(), b.spontaneousCfg(), b.roll()) {
		mode := b.pickMode()
		target, ok := b.pickTarget(chat.ID)
		if (mode == "tease" || mode == "flirt") && !ok {
			mode = "comment"
		}
		b.enqueueSpontaneous(job{
			kind: jobSpontaneous, chatID: chat.ID, mode: mode,
			target: target, targetName: b.targetName(chat.ID, target),
		})
	}
}

// names returns the bot's names and aliases.
func (b *Bot) names() []string {
	return append([]string{b.deps.Cfg.Bot.Name}, b.deps.Cfg.Bot.Aliases...)
}

// storeIncoming persists the message, updates the member and the chat.
func (b *Bot) storeIncoming(m *telego.Message, text string) {
	chatID := m.Chat.ID
	name := m.From.FirstName
	if m.From.LastName != "" {
		name = name + " " + m.From.LastName
	}
	_ = b.deps.DB.AddMessage(chatID, int64(m.MessageID), &store.Message{
		UserID:        m.From.ID,
		Name:          name,
		Username:      m.From.Username,
		Text:          text,
		ReplyToID:     int64(replyMessageID(m)),
		ReplyToUserID: replyUserID(m),
		Date:          time.Unix(m.Date, 0),
	})
	_, _ = b.deps.DB.UpdateMember(chatID, m.From.ID, func(mem *store.Member) {
		mem.FirstName = m.From.FirstName
		mem.LastName = m.From.LastName
		mem.Username = m.From.Username
		mem.LastSeen = time.Unix(m.Date, 0)
		mem.MsgsSinceUpdate++
	})
	if rid := replyMessageID(m); rid != 0 {
		_ = b.deps.DB.UpdateMessage(chatID, int64(rid), func(r *store.Message) { r.Replies++ })
	}
	_ = b.deps.DB.UpdateChat(chatID, func(c *store.Chat) {
		c.Title = m.Chat.Title
		c.MsgsSinceBot++
		c.MsgsSinceGroupUpdate++
	})
}

// maybeEnqueueMemory enqueues a memory update when the §3.9 condition holds.
func (b *Bot) maybeEnqueueMemory(chatID, userID int64) {
	if b.deps.Memory == nil {
		return
	}
	if g := b.deps.Cfg.GroupMemory; g.Enabled {
		if c, err := b.deps.DB.GetChat(chatID); err == nil && c.MsgsSinceGroupUpdate >= g.UpdateEvery {
			b.deps.Memory.EnqueueGroup(chatID)
		}
	}
	mem, err := b.deps.DB.GetMember(chatID, userID)
	if err != nil {
		return
	}
	cfg := b.deps.Cfg.Memory
	if mem.MsgsSinceUpdate >= cfg.UpdateEvery {
		b.deps.Memory.Enqueue(chatID, userID)
		return
	}
	if !mem.ProfileUpdatedAt.IsZero() && time.Since(mem.ProfileUpdatedAt) > cfg.MaxAge && mem.MsgsSinceUpdate >= 3 {
		b.deps.Memory.Enqueue(chatID, userID)
	}
}

// replyMessageID returns the replied-to message id, or 0.
func replyMessageID(m *telego.Message) int {
	if m.ReplyToMessage != nil {
		return m.ReplyToMessage.MessageID
	}
	return 0
}

// replyUserID returns the replied-to user id, or 0.
func replyUserID(m *telego.Message) int64 {
	if m.ReplyToMessage != nil && m.ReplyToMessage.From != nil {
		return m.ReplyToMessage.From.ID
	}
	return 0
}

// spontaneousCfg maps the config's bot section onto the trigger's settings.
func (b *Bot) spontaneousCfg() SpontaneousConfig {
	return SpontaneousConfig{
		Chance:         b.deps.Cfg.Bot.SpontaneousChance,
		Cooldown:       b.deps.Cfg.Bot.SpontaneousCooldown,
		MinMsgsBetween: b.deps.Cfg.Bot.MinMsgsBetween,
	}
}

// roll returns a uniform random float in [0,1) for chance/weight selection.
func (b *Bot) roll() float64 {
	if b.rollFn != nil {
		return b.rollFn()
	}
	return rand.Float64()
}

// pickMode chooses a spontaneous mode by weighted random from the config.
func (b *Bot) pickMode() string { return PickMode(b.deps.Cfg.Bot.Modes, b.roll()) }

// pickTarget picks a spontaneous target among recent authors.
func (b *Bot) pickTarget(chatID int64) (int64, bool) {
	recent, err := b.deps.DB.RecentMessages(chatID, 15)
	if err != nil {
		return 0, false
	}
	members, err := b.deps.DB.Members(chatID)
	if err != nil {
		return 0, false
	}
	mmap := make(map[int64]*store.Member, len(members))
	for _, m := range members {
		mmap[m.UserID()] = m
	}
	return PickTarget(recent, mmap, b.botID, b.roll())
}

// targetName renders the display name of a target user, or "" if unknown.
func (b *Bot) targetName(chatID, target int64) string {
	if target == 0 {
		return ""
	}
	if mem, err := b.deps.DB.GetMember(chatID, target); err == nil {
		return displayName(mem)
	}
	return ""
}

// displayName renders a member's display name (first + last).
func displayName(m *store.Member) string {
	name := m.FirstName
	if m.LastName != "" {
		name = name + " " + m.LastName
	}
	return name
}
