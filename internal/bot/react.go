package bot

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/mymmrac/telego"

	"github.com/ehsanranjbar/hooshang/internal/llm"
	"github.com/ehsanranjbar/hooshang/internal/prompts"
	"github.com/ehsanranjbar/hooshang/internal/store"
)

// reactionEmojis are the emojis Telegram lets a bot react with (the
// ReactionTypeEmoji list of the Bot API).
var reactionEmojis = []string{
	"❤", "👍", "👎", "🔥", "🥰", "👏", "😁", "🤔", "🤯", "😱", "🤬", "😢", "🎉", "🤩", "🤮", "💩", "🙏", "👌", "🕊",
	"🤡", "🥱", "🥴", "😍", "🐳", "❤‍🔥", "🌚", "🌭", "💯", "🤣", "⚡", "🍌", "🏆", "💔", "🤨", "😐", "🍓", "🍾",
	"💋", "🖕", "😈", "😴", "😭", "🤓", "👻", "👨‍💻", "👀", "🎃", "🙈", "😇", "😨", "🤝", "✍", "🤗", "🫡",
	"🎅", "🎄", "☃", "💅", "🤪", "🗿", "🆒", "💘", "🙉", "🦄", "😘", "💊", "🙊", "😎", "👾", "🤷‍♂", "🤷",
	"🤷‍♀", "😡",
}

// reactionEmoji maps the model's emoji to an allowed one: variation selectors
// (❤️ → ❤) and surrounding text are ignored. ok is false if none matches.
func reactionEmoji(s string) (string, bool) {
	s = strings.TrimSpace(strings.ReplaceAll(s, "️", ""))
	for _, e := range reactionEmojis {
		if s == e {
			return e, true
		}
	}
	// The model may add words around it: take the longest allowed emoji it contains.
	best := ""
	for _, e := range reactionEmojis {
		if strings.Contains(s, e) && len(e) > len(best) {
			best = e
		}
	}
	return best, best != ""
}

// react puts an emoji reaction on a message and remembers it, so later
// transcripts show what the bot reacted with.
func (b *Bot) react(ctx context.Context, chatID, msgID int64, emoji string) error {
	err := b.deps.TG.SetMessageReaction(ctx, &telego.SetMessageReactionParams{
		ChatID:    newChatID(chatID),
		MessageID: int(msgID),
		Reaction:  []telego.ReactionType{&telego.ReactionTypeEmoji{Type: telego.ReactionEmoji, Emoji: emoji}},
	})
	if err != nil {
		return err
	}
	_ = b.deps.DB.UpdateMessage(chatID, msgID, func(m *store.Message) { m.BotReaction = emoji })
	return nil
}

// reactTool is the react tool definition.
func reactTool() llm.Tool {
	enum, _ := json.Marshal(reactionEmojis)
	return llm.Tool{
		Name: "react",
		Description: "Put an emoji reaction on a message (default: the >> message). Use it when a reaction says enough " +
			"(a thanks, an ok, a joke that deserves a laugh, a sticker, agreement): then reply with exactly -. " +
			"You may also react and reply.",
		Parameters: json.RawMessage(`{"type":"object","properties":{"emoji":{"type":"string","enum":` + string(enum) +
			`},"message_id":{"type":"integer","description":"the message to react to; default the >> message"}},"required":["emoji"]}`),
	}
}

// toolReact is the react tool handler.
func (b *Bot) toolReact(j job) llm.ToolFunc {
	return func(ctx context.Context, args string) (string, error) {
		var a struct {
			Emoji     string `json:"emoji"`
			MessageID int64  `json:"message_id"`
		}
		if err := json.Unmarshal([]byte(args), &a); err != nil {
			return "error: bad arguments", nil
		}
		emoji, ok := reactionEmoji(a.Emoji)
		if !ok {
			return "error: not an allowed reaction emoji", nil
		}
		id := a.MessageID
		if id == 0 && j.msg != nil {
			id = int64(j.msg.MessageID)
		}
		if id == 0 {
			return "error: no message to react to", nil
		}
		if _, err := b.deps.DB.GetMessage(j.chatID, id); err != nil {
			return "error: no such message", nil
		}
		if err := b.react(ctx, j.chatID, id, emoji); err != nil {
			return "error: " + err.Error(), nil
		}
		return fmt.Sprintf("reacted %s to message %d", emoji, id), nil
	}
}

// ShouldReact reports whether the bot reacts on its own to a message it was
// not addressed by: not muted, past the cooldown, and the roll under the chance.
func ShouldReact(c store.Chat, now time.Time, chance float64, cooldown time.Duration, roll float64) bool {
	if c.Muted || chance <= 0 {
		return false
	}
	if !c.LastReactionAt.IsZero() && now.Sub(c.LastReactionAt) < cooldown {
		return false
	}
	return roll < chance
}

// runReaction runs a spontaneous reaction job: the model picks an emoji for
// the >> message, or "-" for none.
func (b *Bot) runReaction(ctx context.Context, j job) {
	if j.msg == nil {
		return
	}
	_ = b.deps.DB.UpdateChat(j.chatID, func(c *store.Chat) { c.LastReactionAt = time.Now() })
	msgs := b.transcriptMessages(j)
	sys := b.systemPrompt(j.chatID)
	task, _ := prompts.ReactionTask(prompts.TaskData{
		Language:   b.deps.Cfg.Bot.Language,
		Orders:     b.renderOrders(j, msgs),
		Glossary:   b.renderGlossary(j.chatID, b.formatTranscript(j, msgs)),
		Transcript: b.formatTranscript(j, msgs),
		Emojis:     strings.Join(reactionEmojis, " "),
	})
	resp, err := b.deps.LLM.Chat(ctx, llm.Request{
		Model:       b.deps.Cfg.LLM.Model,
		Messages:    []llm.Message{{Role: "system", Content: sys}, {Role: "user", Content: task}},
		Temperature: b.deps.Cfg.LLM.Temperature,
		MaxTokens:   b.deps.Cfg.LLM.MaxTokens,
	})
	if err != nil {
		b.logger.Warn("reaction llm failed", "chat", j.chatID, "err", err)
		return
	}
	out := llm.Clean(resp.Content)
	if out == "" || out == "-" {
		return
	}
	emoji, ok := reactionEmoji(out)
	if !ok {
		b.logger.Debug("reaction: no allowed emoji in the output", "chat", j.chatID, "out", out)
		return
	}
	if err := b.react(ctx, j.chatID, int64(j.msg.MessageID), emoji); err != nil {
		b.logger.Warn("reaction failed", "chat", j.chatID, "err", err)
	}
}
