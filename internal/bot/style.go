package bot

import (
	"strings"
	"time"
	"unicode"

	"github.com/ehsanranjbar/hooshang/internal/store"
)

// laughForms are the ways people laugh in writing, counted per member.
var laughForms = []string{"خخخ", "ههه", "هاها", "😂", "🤣", "💀", "lol", "xd"}

// countStyle folds one message into a member's style stats: words, emoji,
// laugh forms, the hour (Tehran) and who it replies to. Media tags and
// commands are skipped. It is pure, so it may run inside a store update.
func countStyle(s *store.Style, text string, replyTo int64, at time.Time) {
	t := strings.TrimSpace(text)
	if t == "" || strings.HasPrefix(t, "[") || strings.HasPrefix(t, "/") {
		return
	}
	s.Msgs++
	s.Words += len(strings.Fields(t))
	s.Hours[at.Hour()]++
	if hasEmoji(t) {
		s.Emoji++
	}
	low := strings.ToLower(t)
	for _, l := range laughForms {
		if strings.Contains(low, l) {
			if s.Laughs == nil {
				s.Laughs = map[string]int{}
			}
			s.Laughs[l]++
			break
		}
	}
	if replyTo != 0 {
		if s.ReplyTo == nil {
			s.ReplyTo = map[int64]int{}
		}
		s.ReplyTo[replyTo]++
		keepTop(s.ReplyTo, 10)
	}
}

// maxStyleMedia is how many distinct stickers/GIFs a member's style keeps.
const maxStyleMedia = 30

// countMedia folds a sticker or GIF a member sent (by file_unique_id) into
// their style stats, so «استیکر مورد علاقه‌ی فلانی» has an answer.
func countMedia(s *store.Style, uniqueID string) {
	if uniqueID == "" {
		return
	}
	if s.Media == nil {
		s.Media = map[string]int{}
	}
	s.Media[uniqueID]++
	keepTop(s.Media, maxStyleMedia)
}

// keepTop drops the least counted keys until at most n remain.
func keepTop[K comparable](m map[K]int, n int) {
	for len(m) > n {
		var least K
		min := int(^uint(0) >> 1)
		for k, c := range m {
			if c < min {
				least, min = k, c
			}
		}
		delete(m, least)
	}
}

// hasEmoji reports whether the text has an emoji-range rune.
func hasEmoji(s string) bool {
	for _, r := range s {
		if (r >= 0x1F300 && r <= 0x1FAFF) || (r >= 0x2600 && r <= 0x27BF) || unicode.Is(unicode.So, r) {
			return true
		}
	}
	return false
}
