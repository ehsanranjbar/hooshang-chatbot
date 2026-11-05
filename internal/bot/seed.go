package bot

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/mymmrac/telego"

	"github.com/ehsanranjbar/hooshang/internal/i18n"
	"github.com/ehsanranjbar/hooshang/internal/llm"
	"github.com/ehsanranjbar/hooshang/internal/prompts"
	"github.com/ehsanranjbar/hooshang/internal/store"
)

// maybeSeed queues a read of a member's Telegram profile (photos, bio) when
// they are new to the bot or their seed is older than features.profiles.seed.refresh.
// known is the stored member before this message, nil when there was none.
func (b *Bot) maybeSeed(chatID int64, u telego.User, known *store.Member) {
	p := b.deps.Cfg.Features.Profiles
	if !p.On() || (p.Seed.Photos <= 0 && !p.Seed.Bio) || b.describer == nil || u.IsBot {
		return
	}
	if known != nil && !known.SeededAt.IsZero() && (p.Seed.Refresh <= 0 || time.Since(known.SeededAt) < p.Seed.Refresh) {
		return
	}
	if known != nil && known.Ignored {
		return
	}
	key := fmt.Sprintf("seed:%d:%d", chatID, u.ID)
	if !b.describer.submit(key, func(ctx context.Context) { b.seed(ctx, chatID, u) }) {
		b.logger.Debug("profile seed skipped: queue full", "chat", chatID, "user", u.ID)
	}
}

// seed reads what Telegram shows of a member and stores it as items: each
// profile photo described (no identity, just the look) as a "look", the bio
// and birthday as facts. Telegram refuses the bio for users the bot has no
// private chat with; that is normal and logged at debug.
func (b *Bot) seed(ctx context.Context, chatID int64, u telego.User) {
	p := b.deps.Cfg.Features.Profiles
	now := time.Now()
	var items []store.Item
	if p.Seed.Photos > 0 {
		for i, desc := range b.describeProfilePhotos(ctx, u.ID, p.Seed.Photos) {
			items = append(items, store.Item{Kind: store.KindLook, Text: desc, Note: fmt.Sprintf("profile photo %d", i+1),
				Seen: 2, FirstAt: now, LastAt: now, Expires: now.Add(90 * 24 * time.Hour)})
		}
	}
	if p.Seed.Bio {
		if info, err := b.deps.TG.GetChat(ctx, &telego.GetChatParams{ChatID: telego.ChatID{ID: u.ID}}); err == nil && info != nil {
			if bio := strings.TrimSpace(info.Bio); bio != "" {
				items = append(items, store.Item{Kind: store.KindFact, Text: i18n.BioPrefix + oneLine(bio, 120), Seen: 2, FirstAt: now, LastAt: now})
			}
			if bd := info.Birthdate; bd != nil && bd.Month > 0 && bd.Day > 0 {
				items = append(items, store.Item{Kind: store.KindFact, Text: i18n.BirthdayPrefix + birthday(bd), Seen: 2, FirstAt: now, LastAt: now})
			}
		} else if err != nil {
			b.logger.Debug("profile seed: getChat refused", "user", u.ID, "err", err)
		}
	}
	if len(items) > 0 {
		mem, err := b.deps.DB.UpdateMemory(chatID, u.ID, func(m *store.Memory) {
			for _, it := range m.Kind(store.KindLook) {
				m.Remove(it.ID) // the photos are re-read as a whole
			}
			for _, it := range items {
				if cur := findText(m, it.Kind, it.Text); cur != nil {
					cur.LastAt = now
					continue
				}
				m.Add(it)
			}
		})
		if err == nil {
			b.embedItems(chatID, u.ID, mem.Items)
		}
	}
	_, _ = b.deps.DB.UpdateMember(chatID, u.ID, func(m *store.Member) { m.SeededAt = now })
}

// describeProfilePhotos downloads up to n of the member's profile photos and
// has the vision model describe each in a line.
func (b *Bot) describeProfilePhotos(ctx context.Context, userID int64, n int) []string {
	photos, err := b.deps.TG.GetUserProfilePhotos(ctx, &telego.GetUserProfilePhotosParams{UserID: userID, Limit: n})
	if err != nil || photos == nil {
		return nil
	}
	var out []string
	for _, sizes := range photos.Photos {
		if len(sizes) == 0 {
			continue
		}
		best := sizes[len(sizes)-1]
		for _, s := range sizes {
			if max(s.Width, s.Height) >= minPhotoSide {
				best = s
				break
			}
		}
		img, err := b.downloadImage(ctx, best.FileID)
		if err != nil {
			continue
		}
		task, _ := prompts.MediaDescribe(prompts.MediaData{Language: b.deps.Cfg.Bot.Language, Kind: "profile picture of a group member", Words: b.describeWords()})
		resp, err := b.deps.LLM.Chat(ctx, llm.Request{
			Model: b.deps.Cfg.LLM.Model, Messages: []llm.Message{{Role: "user", Content: task, Images: []llm.Image{img}}},
			Temperature: 0.2, MaxTokens: b.deps.Cfg.LLM.MaxTokens,
		})
		if err != nil {
			continue
		}
		if desc := cleanDescription(resp.Content, b.describeWords()); desc != "" {
			out = append(out, desc)
		}
	}
	return out
}

// describeWords is the description length: features.media.words, or 30.
func (b *Bot) describeWords() int {
	if m := b.deps.Cfg.Features.Media; m != nil && m.Words > 0 {
		return m.Words
	}
	return 30
}

// findText returns the item of the kind with the same text, or nil.
func findText(m *store.Memory, kind, textT string) *store.Item {
	for i := range m.Items {
		if m.Items[i].Kind == kind && m.Items[i].Text == textT {
			return &m.Items[i]
		}
	}
	return nil
}

// oneLine squeezes text onto one line of at most n runes.
func oneLine(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > n {
		return string(r[:n]) + "…"
	}
	return s
}

// birthday renders a Telegram birthdate as «۱۴ آبان» (plus the year when given)
// by converting a date in the current year to the Jalali calendar.
func birthday(bd *telego.Birthdate) string {
	year := bd.Year
	if year == 0 {
		year = time.Now().Year()
	}
	t := time.Date(year, time.Month(bd.Month), bd.Day, 12, 0, 0, 0, tehran)
	_, m, d := i18n.Jalali(t)
	out := i18n.PersianDigits(d) + " " + i18n.MonthFa(m)
	if bd.Year != 0 {
		y, _, _ := i18n.Jalali(t)
		out += " " + i18n.PersianDigits(y)
	}
	return out
}
