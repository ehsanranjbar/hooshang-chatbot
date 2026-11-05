package bot

import (
	"github.com/ehsanranjbar/hooshang/internal/text"
	"strings"
	"time"
	"unicode"
	"unicode/utf16"

	"github.com/mymmrac/telego"

	"github.com/ehsanranjbar/hooshang/internal/store"
)

// SpontaneousConfig holds the spontaneous-message settings.
type SpontaneousConfig struct {
	Chance         float64
	Cooldown       time.Duration
	MinMsgsBetween int
}

// IsAddressed reports whether the bot should treat the message as a direct
// request: a private chat, a reply to one of the bot's own messages, an
// @botUsername mention, a text_mention of the bot, or a name/alias appearing
// as a whole word (Persian-aware: the neighbors of the match must not be
// letters; a ZWNJ counts as a letter boundary).
func IsAddressed(m *telego.Message, botID int64, botUsername string, names []string, private bool) bool {
	if private {
		return true
	}
	// Reply to one of the bot's own messages.
	if m.ReplyToMessage != nil && m.ReplyToMessage.From != nil && m.ReplyToMessage.From.ID == botID {
		return true
	}
	text, entities := m.Text, m.Entities
	if text == "" {
		text, entities = m.Caption, m.CaptionEntities
	}
	// Explicit mentions.
	for _, e := range entities {
		if e.Type == telego.EntityTypeMention {
			if strings.EqualFold(entityText(text, e), "@"+botUsername) && botUsername != "" {
				return true
			}
		} else if e.Type == telego.EntityTypeTextMention {
			if u := e.User; u != nil && u.ID == botID {
				return true
			}
		}
	}
	// Name/alias as a whole word.
	for _, name := range names {
		if name == "" {
			continue
		}
		if containsWholeWord(text, name) {
			return true
		}
	}
	return false
}

// entityText returns the substring of text covered by an entity. Telegram
// gives entity offsets and lengths in UTF-16 code units, so an emoji before a
// mention counts as two.
func entityText(text string, e telego.MessageEntity) string {
	u := utf16.Encode([]rune(text))
	if e.Offset < 0 || e.Offset >= len(u) {
		return ""
	}
	end := e.Offset + e.Length
	if end > len(u) {
		end = len(u)
	}
	return string(utf16.Decode(u[e.Offset:end]))
}

// isLetterOrZWNJ reports whether r is a letter or a zero-width non-joiner.
// ZWNJ is treated as a letter boundary stop so "name‍X" never matches.
func isLetterOrZWNJ(r rune) bool {
	return r == '\u200C' || unicode.IsLetter(r)
}

// containsWholeWord reports whether word appears in text as a whole word: the
// runes immediately before and after the match must not be letters (or ZWNJ).
func containsWholeWord(text, word string) bool {
	textL := toLowerFold(text)
	wordL := toLowerFold(word)
	if wordL == "" {
		return false
	}
	rs := []rune(textL)
	wrs := []rune(wordL)
	n := len(wrs)
	for i := 0; i+n <= len(rs); i++ {
		matched := true
		for k := 0; k < n; k++ {
			if rs[i+k] != wrs[k] {
				matched = false
				break
			}
		}
		if !matched {
			continue
		}
		beforeOK := i == 0 || !isLetterOrZWNJ(rs[i-1])
		after := i + n
		afterOK := after == len(rs) || !isLetterOrZWNJ(rs[after])
		if beforeOK && afterOK {
			return true
		}
	}
	return false
}

// toLowerFold lowercases and folds ي→ی and ك→ک, keeping the rune positions
// (text.FoldKeepLen), so the match's neighbours can be inspected.
func toLowerFold(s string) string { return text.FoldKeepLen(s) }

// ShouldSpontaneous reports whether the bot may post a spontaneous message:
// the chat is not muted, the cooldown has elapsed, enough member messages
// have arrived since the bot last spoke, and the roll is under the chance.
func ShouldSpontaneous(c store.Chat, now time.Time, cfg SpontaneousConfig, roll float64) bool {
	if c.Muted {
		return false
	}
	if !c.LastBotAt.IsZero() && now.Sub(c.LastBotAt) < cfg.Cooldown {
		return false
	}
	if c.MsgsSinceBot < cfg.MinMsgsBetween {
		return false
	}
	return roll < cfg.Chance
}

// PickMode chooses a spontaneous mode by weighted random. roll is in [0,1).
// If no mode has a positive weight it falls back to "comment".
func PickMode(weights map[string]int, roll float64) string {
	total := 0
	for _, w := range weights {
		if w > 0 {
			total += w
		}
	}
	if total == 0 {
		return "comment"
	}
	target := roll * float64(total)
	cum := 0.0
	order := []string{"joke", "tease", "flirt", "compliment", "comment", "sticker"}
	for _, mode := range order {
		w := weights[mode]
		if w <= 0 {
			continue
		}
		cum += float64(w)
		if target < cum {
			return mode
		}
	}
	return "comment"
}

// PickTarget picks a spontaneous-message target among the distinct authors of
// the last 15 messages, excluding the bot and members with NoTease. ok is
// false when there is no eligible author.
func PickTarget(recent []store.Message, members map[int64]*store.Member, botID int64, roll float64) (int64, bool) {
	var authors []int64
	seen := make(map[int64]bool)
	for i := len(recent) - 1; i >= 0 && len(recent)-i <= 15; i-- {
		m := recent[i]
		if m.FromBot || m.UserID == botID || seen[m.UserID] {
			continue
		}
		seen[m.UserID] = true
		mem, ok := members[m.UserID]
		if ok && mem != nil && mem.NoTease {
			continue
		}
		authors = append(authors, m.UserID)
	}
	if len(authors) == 0 {
		return 0, false
	}
	// authors is newest-first; index into it with roll.
	idx := int(roll * float64(len(authors)))
	if idx >= len(authors) {
		idx = len(authors) - 1
	}
	return authors[idx], true
}
