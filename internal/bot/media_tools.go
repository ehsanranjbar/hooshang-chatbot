package bot

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/mymmrac/telego"
	tu "github.com/mymmrac/telego/telegoutil"

	"github.com/ehsanranjbar/hooshang/internal/i18n"
	"github.com/ehsanranjbar/hooshang/internal/llm"
	"github.com/ehsanranjbar/hooshang/internal/text"
)

// maxMediaResults is how many stickers/GIFs find_media returns.
const maxMediaResults = 8

// mediaTools are find_media and send_media: the bot answers with a sticker or
// GIF that members have sent before (in any group), found by its description.
func mediaTools() []llm.Tool {
	return []llm.Tool{
		{Name: "find_media", Description: "Search the stickers and GIFs members have sent, by an emoji (😂, 🙄) or a few Persian or English words " +
			"about the reaction or the picture (خنده, تعجب, گربه, facepalm). With member, lists the ones that member sends most (their favourite stickers: «استیکر مورد علاقه فلانی»), query optional. " +
			"Returns ids with their descriptions; then call send_media.",
			Parameters: json.RawMessage(`{"type":"object","properties":{"query":{"type":"string","description":"an emoji or a few words"},"kind":{"type":"string","enum":["sticker","gif","any"]},"member":{"type":"string","description":"a member's name, @username or id, or \"reply\" for the author of the replied-to message: list what they send most"}},"required":[]}`)},
		{Name: "send_media", Description: "Send a sticker or GIF found with find_media, as your answer or alongside it. Use stickers like a person in a group would: now and then, when one says it better than words.",
			Parameters: json.RawMessage(`{"type":"object","properties":{"id":{"type":"string","description":"an id from find_media"}},"required":["id"]}`)},
	}
}

// toolFindMedia is the find_media handler.
func (b *Bot) toolFindMedia(j job) llm.ToolFunc {
	return func(ctx context.Context, args string) (string, error) {
		var a struct {
			Query  string `json:"query"`
			Kind   string `json:"kind"`
			Member string `json:"member"`
		}
		if err := json.Unmarshal([]byte(args), &a); err != nil {
			return "error: bad arguments", nil
		}
		kind := a.Kind
		if kind == "any" {
			kind = ""
		}
		if a.Member != "" {
			return b.memberMedia(j, a.Member, kind, a.Query)
		}
		found, err := b.deps.DB.SearchMedia(kind, a.Query, maxMediaResults)
		if err != nil {
			return "error: " + err.Error(), nil
		}
		if len(found) < maxMediaResults { // then by meaning
			skip := map[string]bool{}
			for _, m := range found {
				skip[m.UniqueID] = true
			}
			found = append(found, b.relatedMedia(ctx, kind, a.Query, maxMediaResults-len(found), skip)...)
		}
		if len(found) == 0 {
			return "nothing found; try another emoji or word, or answer without one", nil
		}
		var sb strings.Builder
		for _, m := range found {
			fmt.Fprintf(&sb, "%s: [%s %s] %s\n", m.UniqueID, m.Kind, m.Emoji, m.Text)
		}
		return strings.TrimSpace(sb.String()), nil
	}
}

// memberMedia answers find_media for one member: the stickers and GIFs they
// sent most in this chat, with how often, optionally only those of a kind or
// matching a query word.
func (b *Bot) memberMedia(j job, member, kind, query string) (string, error) {
	id, err := b.resolveMember(j.chatID, member, j)
	if err != nil {
		return err.Error(), nil
	}
	mem, err := b.deps.DB.GetMember(j.chatID, id)
	if err != nil {
		return "error: unknown member", nil
	}
	ids := make([]string, 0, len(mem.Style.Media))
	for u := range mem.Style.Media {
		ids = append(ids, u)
	}
	sort.Slice(ids, func(a, c int) bool { return mem.Style.Media[ids[a]] > mem.Style.Media[ids[c]] })
	q := text.Fold(query)
	var sb strings.Builder
	n := 0
	for _, u := range ids {
		m, err := b.deps.DB.GetMediaDesc(u)
		if err != nil || m.FileID == "" || (kind != "" && m.Kind != kind) {
			continue
		}
		if q != "" && !strings.Contains(text.Fold(m.Emoji+" "+m.Text), q) {
			continue
		}
		fmt.Fprintf(&sb, "%s: [%s %s] %s (sent %d times)\n", m.UniqueID, m.Kind, m.Emoji, m.Text, mem.Style.Media[u])
		if n++; n >= maxMediaResults {
			break
		}
	}
	if n == 0 {
		return "this member has not sent any sticker or GIF I know of; say so", nil
	}
	return strings.TrimSpace(sb.String()), nil
}

// toolSendMedia is the send_media handler.
func (b *Bot) toolSendMedia(j job) llm.ToolFunc {
	return func(ctx context.Context, args string) (string, error) {
		var a struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal([]byte(args), &a); err != nil {
			return "error: bad arguments", nil
		}
		m, err := b.deps.DB.GetMediaDesc(strings.TrimSpace(a.ID))
		if err != nil || m.FileID == "" {
			return "error: unknown id; use one from find_media", nil
		}
		if !b.deps.Limiter.Reserve(j.chatID, 1) {
			return i18n.RateLimited, nil
		}
		replyTo := 0
		if j.msg != nil {
			replyTo = j.msg.MessageID
		}
		var reply *telego.ReplyParameters
		if replyTo > 0 {
			reply = &telego.ReplyParameters{MessageID: replyTo, AllowSendingWithoutReply: true}
		}
		var sent *telego.Message
		label := i18n.MediaSticker
		if m.Kind == "gif" {
			label = i18n.MediaGIF
			sent, err = b.deps.TG.SendAnimation(ctx, &telego.SendAnimationParams{
				ChatID: newChatID(j.chatID), Animation: tu.FileFromID(m.FileID), ReplyParameters: reply,
			})
		} else {
			sent, err = b.deps.TG.SendSticker(ctx, &telego.SendStickerParams{
				ChatID: newChatID(j.chatID), Sticker: tu.FileFromID(m.FileID), ReplyParameters: reply,
			})
		}
		if err != nil {
			b.logger.Warn("send media failed", "chat", j.chatID, "err", err)
			return "error: could not send it", nil
		}
		// The transcript shows what was sent, as for a member's sticker.
		b.recordSentText(j.chatID, sent, describedTag(tag(label, m.Emoji), m.Text))
		return "sent " + m.Kind + ": " + m.Text, nil
	}
}
