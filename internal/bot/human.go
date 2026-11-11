package bot

import (
	"context"
	"hash/fnv"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf16"

	"github.com/mymmrac/telego"

	"github.com/ehsanranjbar/hooshang/internal/config"
	"github.com/ehsanranjbar/hooshang/internal/memory"
	"github.com/ehsanranjbar/hooshang/internal/prompts"
	"github.com/ehsanranjbar/hooshang/internal/store"
)

// tehran is the bot's clock zone for quiet hours, moods and dates.
var tehran = func() *time.Location {
	if loc, err := time.LoadLocation("Asia/Tehran"); err == nil {
		return loc
	}
	return time.FixedZone("Tehran", 3*3600+30*60)
}()

// systemPrompt renders the system prompt for a chat: persona, notes, the mood
// of the day and the time.
func (b *Bot) systemPrompt(chatID int64) string {
	c := b.deps.Cfg.Bot
	sys, _ := prompts.System(prompts.SystemData{
		Name:     c.Name,
		Language: c.Language,
		Now:      prompts.FormatHour(time.Now()),
		Persona:  c.Persona.Text,
		Notes:    c.Persona.Notes,
		Mood:     prompts.MoodPhrase(b.moodOfDay(chatID, time.Now())),
		Samples:  b.styleSamples(chatID),
	})
	return sys
}

// styleSamples renders the group's anonymised sample messages, if kept.
func (b *Bot) styleSamples(chatID int64) string {
	p := b.deps.Cfg.Features.Profiles
	if !p.On() || p.StyleSamples <= 0 {
		return ""
	}
	mem, err := b.deps.DB.GetMemory(chatID, store.GroupEntity)
	if err != nil {
		return ""
	}
	return memory.RenderSamples(mem)
}

// moodOfDay picks the mood of the day for a chat: the same all day, different
// across days and chats. Returns the mood name ("normal" has no phrase).
func (b *Bot) moodOfDay(chatID int64, now time.Time) string {
	moods := b.deps.Cfg.Bot.Human.Moods
	if len(moods) == 0 {
		moods = prompts.DefaultMoods
	}
	h := fnv.New64a()
	h.Write([]byte(now.In(tehran).Format("2006-01-02")))
	h.Write([]byte(strconv.FormatInt(chatID, 10)))
	return moods[h.Sum64()%uint64(len(moods))]
}

// moodTemperature nudges the sampling temperature with the mood of the day.
func (b *Bot) moodTemperature(chatID int64) float64 {
	t := b.deps.Cfg.LLM.Temperature
	switch b.moodOfDay(chatID, time.Now()) {
	case "sleepy", "grumpy":
		t -= 0.1
	case "hyper":
		t += 0.1
	}
	return max(0.1, min(2, t))
}

// inQuietHours reports whether now (Tehran) is inside bot.human.quiet_hours.
func (b *Bot) inQuietHours(now time.Time) bool {
	from, to, err := config.ParseQuietHours(b.deps.Cfg.Bot.Human.QuietHours)
	if err != nil || b.deps.Cfg.Bot.Human.QuietHours == "" {
		return false
	}
	return quiet(now.In(tehran), from, to)
}

// quiet reports whether the minute of the day of now is in [from, to), a
// range that may cross midnight.
func quiet(now time.Time, from, to int) bool {
	m := now.Hour()*60 + now.Minute()
	if from <= to {
		return m >= from && m < to
	}
	return m >= from || m < to
}

// bubbleSep is the separator the task lets the model write between bubbles,
// --- (or ***), meant on its own line but accepted anywhere between
// whitespace, as the model often tacks it onto the end of a sentence.
var bubbleSep = regexp.MustCompile(`(^|\s)(-{3,}|\*{3,})(\s|$)`)

// splitBubbles splits a reply into chat bubbles on --- separators (and lines
// that are only an em dash), at most max, dropping empty ones.
func splitBubbles(text string, max int) []string {
	if max < 1 {
		max = 1
	}
	var out []string
	var cur []string
	flush := func() {
		if s := strings.TrimSpace(strings.Join(cur, "\n")); s != "" {
			out = append(out, s)
		}
		cur = nil
	}
	text = bubbleSep.ReplaceAllString(text, "\n---\n")
	for _, line := range strings.Split(text, "\n") {
		if t := strings.TrimSpace(line); t == "---" || t == "—" {
			flush()
			continue
		}
		cur = append(cur, line)
	}
	flush()
	if len(out) == 0 {
		return nil
	}
	if len(out) > max {
		out = append(out[:max-1], strings.Join(out[max-1:], "\n"))
	}
	return out
}

// typingDelay is how long a person would take to type text: length over
// chars_per_second, clamped to [min, max]; zero when typing delays are off.
func (b *Bot) typingDelay(text string) time.Duration {
	h := b.deps.Cfg.Bot.Human
	if !h.Typing.Enabled {
		return 0
	}
	d := time.Duration(float64(len([]rune(text))) / float64(h.Typing.CharsPerSecond) * float64(time.Second))
	return max(h.Typing.Min, min(h.Typing.Max, d))
}

// lateDelay sometimes makes the bot answer late, like someone who was away
// from the phone.
func (b *Bot) lateDelay() time.Duration {
	h := b.deps.Cfg.Bot.Human
	if h.LateChance <= 0 || h.LateMax <= 0 || b.roll() >= h.LateChance {
		return 0
	}
	return time.Duration(b.roll() * float64(h.LateMax))
}

// sleep waits d or until ctx is done. Tests replace sleepFn.
func (b *Bot) sleep(ctx context.Context, d time.Duration) {
	if d <= 0 {
		return
	}
	if b.sleepFn != nil {
		b.sleepFn(ctx, d)
		return
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
	case <-t.C:
	}
}

// bubblePause is how long the bot waits between two bubbles: a short pause
// plus typing time, at most 3s.
func bubblePause(next string) time.Duration {
	d := 600*time.Millisecond + time.Duration(len([]rune(next)))*40*time.Millisecond
	return min(d, 3*time.Second)
}

// piece is one message to send: a chunk of a bubble.
type piece struct {
	text      string
	newBubble bool // the first chunk of a bubble: pause before it
}

// deliver sends a finished reply like a person would: converts mentions,
// splits it into bubbles (and over-long bubbles into chunks), reserves one
// limiter slot per message beyond the first (when the limiter refuses, only
// the first chunk of the joined text goes out), waits as if typing, pauses
// between bubbles, and remembers a question asked of someone so their next
// message counts as the answer. The first message replies to replyTo (0 = none).
func (b *Bot) deliver(ctx context.Context, j job, text string, replyTo int) {
	bubbles := splitBubbles(text, b.deps.Cfg.Bot.Human.Bubbles)
	if len(bubbles) == 0 {
		return
	}
	var pieces []piece
	for _, bubble := range bubbles {
		for i, chunk := range chunkText(bubble, maxChunk) {
			pieces = append(pieces, piece{text: chunk, newBubble: i == 0})
		}
	}
	if len(pieces) > 1 && !b.deps.Limiter.Reserve(j.chatID, len(pieces)-1) {
		first := chunkText(strings.Join(bubbles, "\n"), maxChunk)[0]
		pieces = []piece{{text: first}}
		b.logger.Debug("reply cut to one message: rate limited", "chat", j.chatID)
	}
	b.sleep(ctx, b.lateDelay()+b.typingDelay(pieces[0].text))
	for i, p := range pieces {
		if ctx.Err() != nil {
			return
		}
		if i > 0 && p.newBubble {
			_ = b.deps.TG.SendChatAction(ctx, &telego.SendChatActionParams{ChatID: newChatID(j.chatID), Action: telego.ChatActionTyping})
			b.sleep(ctx, bubblePause(p.text))
		}
		rt := 0
		if i == 0 {
			rt = replyTo
		}
		plain, entities := b.mentions(j.chatID, p.text)
		if err := b.sendOne(ctx, j.chatID, rt, plain, entities); err != nil {
			return
		}
	}
	b.rememberQuestion(j, pieces[len(pieces)-1].text)
}

// rememberQuestion stores a question the bot just asked the trigger's author.
func (b *Bot) rememberQuestion(j job, last string) {
	if j.msg == nil || j.msg.From == nil {
		return
	}
	t := strings.TrimSpace(last)
	if !strings.HasSuffix(t, "؟") && !strings.HasSuffix(t, "?") {
		return
	}
	_ = b.deps.DB.UpdateChat(j.chatID, func(c *store.Chat) {
		c.LastBotQuestion = store.BotQuestion{UserID: j.msg.From.ID, MsgID: int64(j.msg.MessageID), At: time.Now()}
	})
}

// followUpWindow is how long after the bot asks someone a question their next
// message counts as the answer, even without the bot's name.
const followUpWindow = 2 * time.Minute

// isFollowUp reports whether m answers a question the bot asked its author,
// and forgets the question so only one message counts.
func (b *Bot) isFollowUp(c store.Chat, m *telego.Message) bool {
	q := c.LastBotQuestion
	if m.From == nil || q.UserID == 0 || q.UserID != m.From.ID || time.Since(q.At) > followUpWindow {
		return false
	}
	_ = b.deps.DB.UpdateChat(m.Chat.ID, func(c *store.Chat) { c.LastBotQuestion = store.BotQuestion{} })
	return true
}

// mentionMarker is how the model mentions a member without a username.
var mentionMarker = regexp.MustCompile(`\{mention:(\d+)\}`)

// mentions replaces {mention:ID} markers with the member's first name carrying
// a text_mention entity (offsets in UTF-16 code units, as Telegram wants).
// Unknown ids are dropped.
func (b *Bot) mentions(chatID int64, text string) (string, []telego.MessageEntity) {
	if !strings.Contains(text, "{mention:") {
		return text, nil
	}
	var out strings.Builder
	var entities []telego.MessageEntity
	last := 0
	for _, loc := range mentionMarker.FindAllStringSubmatchIndex(text, -1) {
		out.WriteString(text[last:loc[0]])
		last = loc[1]
		id, _ := strconv.ParseInt(text[loc[2]:loc[3]], 10, 64)
		mem, err := b.deps.DB.GetMember(chatID, id)
		if err != nil || mem == nil || mem.FirstName == "" {
			continue
		}
		offset := len(utf16.Encode([]rune(out.String())))
		out.WriteString(mem.FirstName)
		entities = append(entities, telego.MessageEntity{
			Type: telego.EntityTypeTextMention, Offset: offset,
			Length: len(utf16.Encode([]rune(mem.FirstName))), User: &telego.User{ID: id, FirstName: mem.FirstName},
		})
	}
	out.WriteString(text[last:])
	return strings.Join(strings.Fields(out.String()), " "), entities
}
