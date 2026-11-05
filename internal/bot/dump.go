package bot

import (
	"fmt"
	"log/slog"
	"time"

	"github.com/mymmrac/telego"

	"github.com/ehsanranjbar/hooshang/internal/llm"
)

// DumpPrompt returns the request a reply to a stored message would send to
// the model, built by the same code as a real reply, with the chat as it was
// when the message arrived (the system prompt's clock is still now). messageID 0 means the
// latest member message of the chat. With full, the chance-gated context (the
// author's profile, the group memory) is always included; otherwise it is
// left out, as in most real replies. Mentions are not stored, so the profiles
// of mentioned members are missing.
func DumpPrompt(d Deps, botID, chatID, messageID int64, full bool) (llm.Request, error) {
	b := &Bot{deps: d, logger: slog.Default(), botID: botID, accessCache: map[int64]accessEntry{}}
	b.rollFn = func() float64 { return 1 } // never passes a chance check
	if full {
		b.rollFn = func() float64 { return 0 } // always passes
	}
	msgs, err := d.DB.RecentMessages(chatID, 500)
	if err != nil {
		return llm.Request{}, err
	}
	var m *telego.Message
	for i := len(msgs) - 1; i >= 0; i-- {
		s := msgs[i]
		if (messageID == 0 && !s.FromBot) || s.ID == messageID {
			m = &telego.Message{
				MessageID: int(s.ID),
				Chat:      telego.Chat{ID: chatID, Type: "supergroup"},
				From:      &telego.User{ID: s.UserID, FirstName: s.Name, Username: s.Username},
				Text:      s.Text,
				Date:      s.Date.Unix(),
			}
			if s.ReplyToID > 0 {
				m.ReplyToMessage = &telego.Message{MessageID: int(s.ReplyToID), From: &telego.User{ID: s.ReplyToUserID}}
			}
			break
		}
	}
	if m == nil {
		return llm.Request{}, fmt.Errorf("no such message in chat %d", chatID)
	}
	req, _ := b.replyRequest(job{kind: jobReply, chatID: chatID, msg: m, asOf: time.Unix(m.Date, 0)})
	return req, nil
}
