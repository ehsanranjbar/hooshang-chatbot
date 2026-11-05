package bot

import (
	"context"
	"math/rand/v2"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/mymmrac/telego"

	"github.com/ehsanranjbar/hooshang/internal/ganjoor"
	"github.com/ehsanranjbar/hooshang/internal/i18n"
	"github.com/ehsanranjbar/hooshang/internal/llm"
	"github.com/ehsanranjbar/hooshang/internal/memory"
	"github.com/ehsanranjbar/hooshang/internal/prompts"
)

// handleFaal runs the /faal [نیت] flow (§8). It reserves 2 slots and stops if
// it cannot.
func (b *Bot) handleFaal(ctx context.Context, m *telego.Message, wish string) {
	if !b.deps.Limiter.Reserve(m.Chat.ID, 2) {
		b.logger.Debug("faal rate limited", "chat", m.Chat.ID)
		return
	}
	b.runFaal(ctx, m.Chat.ID, m.MessageID, m.From.ID, wish)
}

// runFaal performs the two-message faal flow and returns the poem's full title
// (or "" if it failed). The caller must already hold the needed slots.
func (b *Bot) runFaal(ctx context.Context, chatID int64, replyTo int, askerID int64, wish string) string {
	if b.deps.Ganjoor == nil {
		return ""
	}
	poem, err := b.deps.Ganjoor.Faal(ctx)
	if err != nil {
		b.logger.Warn("faal fetch failed", "chat", chatID, "err", err)
		_ = b.send(ctx, chatID, replyTo, i18n.GanjoorDown)
		return ""
	}

	// Message 1: the ghazal (as a Nastaliq image, or as text) with its معنی.
	// It is sent first, so the asker reads it while the تعبیر is written.
	if err := b.sendFaalPoem(ctx, chatID, replyTo, poem); err != nil {
		return ""
	}

	// Message 2: the LLM's interpretation.
	if tabir := b.runFaalTabir(ctx, chatID, askerID, poem, wish); tabir != "" {
		_ = b.send(ctx, chatID, replyTo, i18n.TabirPrefix+" "+tabir)
	}
	return poem.FullTitle
}

// captionLimit is Telegram's caption limit (1024), with a margin.
const captionLimit = 1000

// sendFaalPoem sends the ghazal: an image with the معنی as its caption when a
// renderer is configured (falling back to text if rendering fails), else text.
func (b *Bot) sendFaalPoem(ctx context.Context, chatID int64, replyTo int, poem *ganjoor.Poem) error {
	if r := b.deps.FaalImage; r != nil {
		png, err := r.Render(i18n.FaalTitle+" — "+strings.Join(strings.Fields(poem.Title), " "), poem.Hemistichs)
		if err == nil {
			return b.sendPhoto(ctx, chatID, replyTo, png, "faal.png", faalCaption(poem), faalText(poem))
		}
		b.logger.Warn("faal image failed, sending text", "err", err)
	}
	return b.send(ctx, chatID, replyTo, faalText(poem))
}

// faalText is the faal as text: the whole ghazal, its معنی and the link. It is
// sent when there is no image, and stored with the image so the model sees
// the poem in later transcripts.
func faalText(poem *ganjoor.Poem) string {
	text := i18n.FaalHeader + "\n" + ganjoor.Format(poem, 0)
	if poem.Summary != "" {
		text += "\n\n" + i18n.MeaningLabel + "\n" + poem.Summary
	}
	return text + "\n\n" + i18n.PoemLink + " " + poem.URL
}

// faalCaption is the photo caption: the header, the معنی (shortened at a
// sentence end to fit Telegram's caption limit) and the link.
func faalCaption(p *ganjoor.Poem) string {
	head := i18n.FaalHeader + " — " + strings.Join(strings.Fields(p.FullTitle), " ")
	tail := "\n\n" + i18n.PoemLink + " " + p.URL
	if p.Summary == "" {
		return head + tail
	}
	room := captionLimit - len([]rune(head+tail)) - len([]rune("\n\n"+i18n.MeaningLabel+"\n"))
	summary := []rune(p.Summary)
	if len(summary) > room {
		cut := string(summary[:max(0, room-1)])
		if i := strings.LastIndexAny(cut, ".؛!؟"); i > len(cut)/2 {
			_, size := utf8.DecodeRuneInString(cut[i:]) // «؟» and «؛» are two bytes
			cut = cut[:i+size]
		} else {
			cut += "…"
		}
		summary = []rune(cut)
	}
	return head + "\n\n" + i18n.MeaningLabel + "\n" + string(summary) + tail
}

// runFaalTabir asks the LLM (no tools) for a 2–4 sentence interpretation. Its
// message is covered by the 2 slots the faal caller reserved; on any failure
// it returns "" (message 2 is skipped).
func (b *Bot) runFaalTabir(ctx context.Context, chatID int64, askerID int64, poem *ganjoor.Poem, wish string) string {
	profile, asker := "", ""
	if mem, err := b.deps.DB.GetMember(chatID, askerID); err == nil && mem != nil {
		profile, asker = memory.Render(mem.Profile), displayName(mem)
	}
	sys, _ := prompts.System(prompts.SystemData{
		Name:     b.deps.Cfg.Bot.Name,
		Language: b.deps.Cfg.Bot.Language,
		Now:      prompts.FormatNow(time.Now()),
	})
	task, err := prompts.FaalTabir(prompts.FaalData{
		Language: b.deps.Cfg.Bot.Language,
		Poem:     ganjoor.Format(poem, 0),
		Summary:  poem.Summary,
		Asker:    asker,
		Profile:  profile,
		Wish:     wish,
	})
	if err != nil {
		return ""
	}
	resp, err := b.deps.LLM.Chat(ctx, llm.Request{
		Model:       b.deps.Cfg.LLM.Model,
		Messages:    []llm.Message{{Role: "system", Content: sys}, {Role: "user", Content: task}},
		Temperature: b.deps.Cfg.LLM.Temperature,
		MaxTokens:   b.deps.Cfg.LLM.MaxTokens,
	})
	if err != nil {
		return ""
	}
	return cleanReply(resp.Content, b.deps.Cfg.Bot.Name)
}

// handlePoem runs the /poem [شاعر] flow (§8). It reserves 1 slot and stops if
// it cannot.
func (b *Bot) handlePoem(ctx context.Context, m *telego.Message, poet string) {
	if b.deps.Ganjoor == nil {
		return
	}
	poetID, ok := poetFor(poet)
	if !ok {
		b.replyCommand(m, i18n.UnknownPoet)
		return
	}
	if !b.deps.Limiter.Reserve(m.Chat.ID, 1) {
		b.logger.Debug("poem rate limited", "chat", m.Chat.ID)
		return
	}
	poem, err := b.deps.Ganjoor.RandomPoem(ctx, poetID)
	if err != nil {
		b.logger.Warn("poem fetch failed", "chat", m.Chat.ID, "err", err)
		_ = b.send(ctx, m.Chat.ID, m.MessageID, i18n.GanjoorDown)
		return
	}
	_ = b.sendPoem(ctx, m.Chat.ID, m.MessageID, poem)
}

// sendPoem sends a poem's first two بیت: as a Nastaliq image in the faal's
// style, captioned with the poet, the title and the link, when a renderer is
// configured (falling back to text if rendering fails), else as text.
func (b *Bot) sendPoem(ctx context.Context, chatID int64, replyTo int, p *ganjoor.Poem) error {
	if r := b.deps.PoemImage; r != nil {
		hs := p.Hemistichs[:min(len(p.Hemistichs), 2*poemCouplets)]
		title := p.Poet
		if t := strings.Join(strings.Fields(p.Title), " "); t != "" {
			title += " — " + t
		}
		png, err := r.Render(title, hs)
		if err == nil {
			return b.sendPhoto(ctx, chatID, replyTo, png, "poem.png", poemCredit(p)+"\n"+i18n.PoemLink+" "+p.URL, poemMessage(p))
		}
		b.logger.Warn("poem image failed, sending text", "err", err)
	}
	return b.send(ctx, chatID, replyTo, poemMessage(p))
}

// poemCouplets is how many بیت /poem and the random_poem tool send.
const poemCouplets = 2

// poemMessage renders a poem for /poem and the random_poem tool: its first
// two بیت, then the poet and the poem's title, then the link.
func poemMessage(p *ganjoor.Poem) string {
	return "📜 " + ganjoor.Excerpt(p, poemCouplets) + "\n\n" + poemCredit(p) + "\n" + i18n.PoemLink + " " + p.URL
}

// poemCredit is "— {poet}، {title}".
func poemCredit(p *ganjoor.Poem) string {
	credit := "— " + p.Poet
	if p.Title != "" {
		credit += "، " + strings.Join(strings.Fields(p.Title), " ")
	}
	return credit
}

// poetFor resolves a poet name; an empty name picks a random known poet.
func poetFor(name string) (int, bool) {
	if strings.TrimSpace(name) == "" {
		names := ganjoor.PoetNames()
		return ganjoor.PoetID(names[rand.IntN(len(names))])
	}
	return ganjoor.PoetID(name)
}

// handleRoast turns /roast into a reply job with a roast task. The target is
// the replied-to user or a @mention.
func (b *Bot) handleRoast(ctx context.Context, m *telego.Message) {
	target, ok := b.commandTarget(m)
	if !ok {
		b.replyCommand(m, i18n.RoastWho)
		return
	}
	if mem, err := b.deps.DB.GetMember(m.Chat.ID, target); err == nil && mem != nil && mem.NoTease {
		b.replyCommand(m, i18n.NoTeaseTarget)
		return
	}
	b.enqueueReply(job{
		kind:       jobReply,
		chatID:     m.Chat.ID,
		msg:        m,
		mode:       "roast",
		target:     target,
		targetName: b.targetName(m.Chat.ID, target),
	})
}

// commandTarget resolves the target of /roast: the replied-to user, or the
// first @mention / text_mention in the message.
func (b *Bot) commandTarget(m *telego.Message) (int64, bool) {
	if m.ReplyToMessage != nil && m.ReplyToMessage.From != nil {
		return m.ReplyToMessage.From.ID, true
	}
	text := m.Text
	for _, e := range m.Entities {
		switch e.Type {
		case telego.EntityTypeMention:
			name := strings.TrimPrefix(entityText(text, e), "@")
			if mem, err := b.deps.DB.MemberByUsername(m.Chat.ID, name); err == nil && mem != nil {
				return mem.UserID(), true
			}
		case telego.EntityTypeTextMention:
			if e.User != nil {
				return e.User.ID, true
			}
		}
	}
	return 0, false
}
