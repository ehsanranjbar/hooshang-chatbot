package bot

import (
	"bytes"
	"context"
	"github.com/ehsanranjbar/hooshang/internal/i18n"
	"strings"
	"time"

	"github.com/mymmrac/telego"
	tu "github.com/mymmrac/telego/telegoutil"

	"github.com/ehsanranjbar/hooshang/internal/store"
)

// maxChunk is the maximum number of runes per sent message.
const maxChunk = 4000

// send is the ONLY function that calls SendMessage. It splits text into
// chunks of at most 4000 runes on line breaks, replies to replyTo when it is
// greater than 0, and stores each sent message (FromBot) plus updates the
// chat (LastBotAt, MsgsSinceBot=0). On a 429 it logs and gives up (no retry).
//
// Callers must have reserved limiter slots first — one per chunk.
func (b *Bot) send(ctx context.Context, chatID int64, replyTo int, text string) error {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil
	}
	return b.sendChunks(ctx, chatID, replyTo, chunkText(text, maxChunk))
}

// sendChunks sends exactly the given chunks (one reserved limiter slot each).
func (b *Bot) sendChunks(ctx context.Context, chatID int64, replyTo int, chunks []string) error {
	for _, chunk := range chunks {
		p := &telego.SendMessageParams{
			ChatID: newChatID(chatID),
			Text:   chunk,
		}
		if replyTo > 0 {
			p.ReplyParameters = &telego.ReplyParameters{
				MessageID:                replyTo,
				AllowSendingWithoutReply: true,
			}
		}
		msg, err := b.deps.TG.SendMessage(ctx, p)
		if err != nil {
			if isFloodControl(err) {
				b.logger.Warn("send: flood control (429), giving up", "chat", chatID)
			} else {
				b.logger.Error("send failed", "chat", chatID, "err", err)
			}
			return err
		}
		b.recordSent(chatID, msg)
	}
	return nil
}

// sendPhoto sends a PNG with a caption (one reserved limiter slot). Like
// send, it is the only place that calls SendPhoto.
func (b *Bot) sendPhoto(ctx context.Context, chatID int64, replyTo int, png []byte, name, caption, record string) error {
	p := &telego.SendPhotoParams{
		ChatID:  newChatID(chatID),
		Photo:   tu.File(tu.NameReader(bytes.NewReader(png), name)),
		Caption: caption,
	}
	if replyTo > 0 {
		p.ReplyParameters = &telego.ReplyParameters{MessageID: replyTo, AllowSendingWithoutReply: true}
	}
	msg, err := b.deps.TG.SendPhoto(ctx, p)
	if err != nil {
		b.logger.Error("send photo failed", "chat", chatID, "err", err)
		return err
	}
	if record != "" && msg != nil {
		b.recordSentText(chatID, msg, "["+i18n.MediaPhoto+"] "+record)
	} else {
		b.recordSent(chatID, msg)
	}
	return nil
}

// recordSent stores the sent message and updates the chat's bot state.
func (b *Bot) recordSent(chatID int64, msg *telego.Message) {
	if msg == nil {
		return
	}
	b.recordSentText(chatID, msg, sentText(msg))
}

// recordSentText stores a message the bot sent, with text as what the model
// will see of it (an image's caption is not enough: a poem card stores its
// verses).
func (b *Bot) recordSentText(chatID int64, msg *telego.Message, text string) {
	if msg == nil {
		return
	}
	mid := int64(msg.MessageID)
	date := time.Unix(msg.Date, 0)
	if msg.Date == 0 {
		date = time.Now()
	}
	_ = b.deps.DB.AddMessage(chatID, mid, &store.Message{
		UserID:   b.botID,
		Name:     b.deps.Cfg.Bot.Name,
		Username: b.botUsername,
		Text:     text,
		Date:     date,
		FromBot:  true,
	})
	_ = b.deps.DB.UpdateChat(chatID, func(c *store.Chat) {
		c.LastBotAt = date
		c.MsgsSinceBot = 0
	})
}

// sentText is how a message the bot sent appears in the transcript.
func sentText(msg *telego.Message) string {
	if t, _ := messageText(msg); t != "" {
		return t
	}
	return msg.Text
}

// chunkText splits text into at most max-rune chunks, breaking on line
// boundaries where possible.
func chunkText(text string, max int) []string {
	rs := []rune(text)
	if len(rs) <= max {
		return []string{text}
	}
	var chunks []string
	start := 0
	for start < len(rs) {
		end := start + max
		if end >= len(rs) {
			chunks = append(chunks, string(rs[start:]))
			break
		}
		// Prefer to break on the last newline within the window.
		breakAt := -1
		for i := end; i > start; i-- {
			if rs[i-1] == '\n' {
				breakAt = i - 1
				break
			}
		}
		if breakAt <= start {
			breakAt = end
		}
		chunks = append(chunks, string(rs[start:breakAt]))
		start = breakAt
		// Skip the newline we broke on.
		if start < len(rs) && rs[start] == '\n' {
			start++
		}
	}
	return chunks
}
