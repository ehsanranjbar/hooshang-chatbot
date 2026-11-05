package bot

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/mymmrac/telego"

	"github.com/ehsanranjbar/hooshang/internal/llm"
	"github.com/ehsanranjbar/hooshang/internal/memory"
	"github.com/ehsanranjbar/hooshang/internal/prompts"
	"github.com/ehsanranjbar/hooshang/internal/store"
)

// runReply runs a reply job (§3.9 reply.go step 1–7).
func (b *Bot) runReply(ctx context.Context, j job) {
	// 1. Reserve first: a reply needs one slot.
	if !b.deps.Limiter.Reserve(j.chatID, 1) {
		b.logger.Debug("reply dropped: rate limited", "chat", j.chatID)
		return
	}

	// A photo or sticker the bot is asked about is described first.
	pre := b.describeTrigger(ctx, j.msg)
	b.transcribeTrigger(ctx, j.msg) // a voice message, before it is read

	// 2–5. The prompt and the tools.
	answered := false
	j.answered = &answered
	req, handlers := b.replyRequest(j)
	if imgs, id, ok := b.questionImage(ctx, j.msg, pre); ok {
		req.Messages[1].Images = imgs
		req.Messages[1].Content += "\n\n" + imageNote(id, len(imgs))
	}

	var resp llm.Response
	var err error
	if len(req.Tools) > 0 {
		resp, err = llm.RunTools(ctx, b.deps.LLM, req, handlers, 3)
	} else {
		resp, err = b.deps.LLM.Chat(ctx, req)
	}
	if err != nil {
		b.logger.Warn("llm call failed", "chat", j.chatID, "err", err)
		return
	}

	// 6. Post-process. After a faal the poem and its تعبیر are the answer:
	// models don't reliably stay silent when told to, so the code does.
	if answered {
		return
	}
	// The model sometimes writes the transcript's "{you reacted 😂}" instead of
	// calling react: do the reaction it meant, and don't send the marker.
	content, marked := splitReactionMarkers(resp.Content)
	if len(marked) > 0 && j.msg != nil {
		if s, err := b.deps.DB.GetMessage(j.chatID, int64(j.msg.MessageID)); err == nil && s.BotReaction == "" {
			if err := b.react(ctx, j.chatID, int64(j.msg.MessageID), marked[0]); err != nil {
				b.logger.Warn("reaction failed", "chat", j.chatID, "err", err)
			}
		}
	}
	text := cleanReply(content, b.deps.Cfg.Bot.Name)
	if text == "" {
		return
	}

	// 7. Reserve extra chunks, then send.
	chunks := chunkText(text, maxChunk)
	replyTo := 0
	if j.kind == jobReply && j.msg != nil {
		replyTo = j.msg.MessageID
	}
	if len(chunks) > 1 && !b.deps.Limiter.Reserve(j.chatID, len(chunks)-1) {
		chunks = chunks[:1]
		b.logger.Debug("reply truncated: rate limited on extra chunks", "chat", j.chatID)
	}
	_ = b.sendChunks(ctx, j.chatID, replyTo, chunks)
}

// replyRequest builds the LLM request of a reply job: the system prompt (plus
// the admin rules for the admin), the task with the profiles, group memory,
// glossary and transcript, and the tools the author may use.
func (b *Bot) replyRequest(j job) (llm.Request, map[string]llm.ToolFunc) {
	msgs := b.transcriptMessages(j)
	transcript := b.formatTranscript(j, msgs)
	sys, _ := prompts.System(prompts.SystemData{
		Name:     b.deps.Cfg.Bot.Name,
		Language: b.deps.Cfg.Bot.Language,
		Now:      prompts.FormatNow(time.Now()),
	})
	if j.authorID() > 0 && b.deps.Admins.IsAdmin(j.chatID, j.authorID()) {
		sys += prompts.AdminRules(b.deps.Cfg.Features.Profiles.On()) // the edit_memory rules only with profiles
	}
	userMsg, _ := prompts.ReplyTask(prompts.TaskData{
		Language:   b.deps.Cfg.Bot.Language,
		Orders:     b.renderOrders(j, msgs),
		Profiles:   b.renderProfiles(j, msgs),
		Group:      b.renderGroupMemory(j.chatID, j),
		Glossary:   b.renderGlossary(j.chatID, transcript),
		Transcript: transcript,
		Task:       b.taskFor(j),
		Variety:    b.varietyNote(j.chatID),
	})
	tools, handlers := b.toolsFor(j)
	return llm.Request{
		Model:       b.deps.Cfg.LLM.Model,
		Messages:    []llm.Message{{Role: "system", Content: sys}, {Role: "user", Content: userMsg}},
		Temperature: b.deps.Cfg.LLM.Temperature,
		MaxTokens:   b.deps.Cfg.LLM.MaxTokens,
		Tools:       tools,
	}, handlers
}

// runSpontaneous runs a spontaneous job.
func (b *Bot) runSpontaneous(ctx context.Context, j job) {
	// 1. A spontaneous message needs the chat to be under half its per-minute
	// cap, then reserves one slot.
	_, chatFree := b.deps.Limiter.Free(j.chatID)
	if chatFree*2 < b.deps.Limiter.ChatCap() {
		b.logger.Debug("spontaneous dropped: rate limited", "chat", j.chatID)
		return
	}
	if !b.deps.Limiter.Reserve(j.chatID, 1) {
		b.logger.Debug("spontaneous dropped: rate limited", "chat", j.chatID)
		return
	}

	msgs := b.transcriptMessages(j)
	transcript := b.formatTranscript(j, msgs)
	profiles := b.renderProfiles(j, msgs)

	sys, _ := prompts.System(prompts.SystemData{
		Name:     b.deps.Cfg.Bot.Name,
		Language: b.deps.Cfg.Bot.Language,
		Now:      prompts.FormatNow(time.Now()),
	})
	task, _ := prompts.SpontaneousTask(prompts.TaskData{
		Language:   b.deps.Cfg.Bot.Language,
		Orders:     b.renderOrders(j, msgs),
		Profiles:   profiles,
		Group:      b.renderGroupMemory(j.chatID, j),
		Glossary:   b.renderGlossary(j.chatID, transcript),
		Transcript: transcript,
		Mode:       j.mode,
		Target:     j.targetName,
		Variety:    b.varietyNote(j.chatID),
	})
	req := llm.Request{
		Model:       b.deps.Cfg.LLM.Model,
		Messages:    []llm.Message{{Role: "system", Content: sys}, {Role: "user", Content: task}},
		Temperature: b.deps.Cfg.LLM.Temperature,
		MaxTokens:   b.deps.Cfg.LLM.MaxTokens,
	}
	resp, err := b.deps.LLM.Chat(ctx, req)
	if err != nil {
		b.logger.Warn("spontaneous llm failed", "chat", j.chatID, "err", err)
		return
	}
	content, _ := splitReactionMarkers(resp.Content)
	text := cleanReply(content, b.deps.Cfg.Bot.Name)
	if text == "" {
		return // the model said "-" or produced nothing
	}
	chunks := chunkText(text, maxChunk)
	if len(chunks) > 1 && !b.deps.Limiter.Reserve(j.chatID, len(chunks)-1) {
		chunks = chunks[:1]
	}
	// Spontaneous messages are not replies.
	_ = b.sendChunks(ctx, j.chatID, 0, chunks)
}

// authorID returns the triggering message's author id, or 0.
func (j job) authorID() int64 {
	if j.msg != nil && j.msg.From != nil {
		return j.msg.From.ID
	}
	return 0
}

// taskFor is the task line of a reply job.
func (b *Bot) taskFor(j job) string {
	if j.mode == "roast" {
		name := j.targetName
		if name == "" {
			name = "someone"
		}
		return "Roast " + name + " with real bite (the >> message asked for it). Don't roast the requester."
	}
	if j.msg == nil || j.msg.From == nil {
		return "Reply to the conversation."
	}
	task := "Reply to the >> message from " + displayNameOf(j.msg) + ". If it asks a real question (a fact, a person, the group, a term), answer it plainly and politely, with no jab. If it insults or mocks you, hit back hard. If a reaction is all it needs, call react and reply with exactly -."
	if b.deps.Cfg.Features.Profiles.On() && b.deps.Admins.IsAdmin(j.chatID, j.msg.From.ID) {
		task += " " + displayNameOf(j.msg) + " is the admin: if this message tells you to remember, forget or correct something about a member, call edit_memory first."
	}
	return task
}

// displayNameOf renders the display name of a telego message's author.
func displayNameOf(m *telego.Message) string {
	if m.From == nil {
		return "someone"
	}
	name := m.From.FirstName
	if m.From.LastName != "" {
		name += " " + m.From.LastName
	}
	return name
}

// renderTranscript renders the conversation the bot is reacting to: the latest
// messages that are inside the history window (old topics drop out), plus the
// message being replied to if it is older. The triggering message is marked >>.
func (b *Bot) renderTranscript(j job) string {
	return b.formatTranscript(j, b.transcriptMessages(j))
}

// transcriptMessages returns the messages renderTranscript shows.
func (b *Bot) transcriptMessages(j job) []store.Message {
	limit := b.deps.Cfg.Bot.HistoryMessages
	trigger, replyTo := int64(0), int64(0)
	if j.msg != nil {
		trigger, replyTo = int64(j.msg.MessageID), int64(replyMessageID(j.msg))
	}
	now := time.Now()
	fetch := limit
	if !j.asOf.IsZero() {
		now, fetch = j.asOf, 1000
	}
	msgs, err := b.deps.DB.RecentMessages(j.chatID, fetch)
	if err != nil {
		return nil
	}
	if !j.asOf.IsZero() && trigger != 0 { // the chat as it was at the trigger
		for i, m := range msgs {
			if m.ID > trigger {
				msgs = msgs[:i]
				break
			}
		}
		if len(msgs) > limit {
			msgs = msgs[len(msgs)-limit:]
		}
	}
	cutoff := now.Add(-b.deps.Cfg.Bot.HistoryWindow)
	var kept []store.Message
	hasTrigger, hasReplyTo := false, false
	for _, m := range msgs {
		if m.Date.Before(cutoff) && m.ID != trigger && m.ID != replyTo {
			continue
		}
		hasTrigger = hasTrigger || m.ID == trigger
		hasReplyTo = hasReplyTo || m.ID == replyTo
		kept = append(kept, m)
	}
	// In a busy chat the trigger can fall out of the last messages before
	// its job runs; the model must still see what it is answering.
	if trigger != 0 && !hasTrigger {
		if m, err := b.deps.DB.GetMessage(j.chatID, trigger); err == nil {
			kept = append(kept, *m)
		}
	}
	if replyTo != 0 && !hasReplyTo {
		if m, err := b.deps.DB.GetMessage(j.chatID, replyTo); err == nil {
			kept = append([]store.Message{*m}, kept...)
		}
	}

	return kept
}

// formatTranscript renders messages as transcript lines, the trigger marked >>.
func (b *Bot) formatTranscript(j job, kept []store.Message) string {
	trigger := int64(0)
	if j.msg != nil {
		trigger = int64(j.msg.MessageID)
	}
	var sb strings.Builder
	for _, m := range kept {
		if m.ID == trigger && trigger != 0 {
			sb.WriteString(">> ")
		}
		fmt.Fprintf(&sb, "[%d] %s", m.ID, m.Name)
		if m.Username != "" {
			sb.WriteString(" (@" + m.Username + ")")
		}
		if m.FromBot {
			sb.WriteString(" (you)")
		}
		if m.ReplyToID > 0 {
			fmt.Fprintf(&sb, " ↩︎[%d]", m.ReplyToID)
		}
		sb.WriteString(": " + m.Text)
		if m.BotReaction != "" {
			sb.WriteString(" {you reacted " + m.BotReaction + "}")
		}
		sb.WriteByte('\n')
	}
	return strings.TrimSpace(sb.String())
}

// renderProfiles renders the member profiles the bot may draw on, in order,
// while they fit in bot.profile_budget characters:
//   - the people the message is about (reply target, mentions, names, the
//     roast or spontaneous target) and the author: always, even over budget;
//   - the members who wrote in the transcript, latest first;
//   - up to bot.random_profiles random other members.
func (b *Bot) renderProfiles(j job, msgs []store.Message) string {
	if !b.deps.Cfg.Features.Profiles.On() {
		return ""
	}
	seen := map[int64]bool{0: true, b.botID: true}
	var must, more []int64
	add := func(list *[]int64, id int64) {
		if !seen[id] {
			seen[id] = true
			*list = append(*list, id)
		}
	}
	if j.msg != nil && j.msg.From != nil {
		for _, id := range b.aboutIDs(j) {
			add(&must, id)
		}
		add(&must, j.msg.From.ID)
	}
	add(&must, j.target)
	for i := len(msgs) - 1; i >= 0; i-- {
		if !msgs[i].FromBot {
			add(&more, msgs[i].UserID)
		}
	}
	if n := b.deps.Cfg.Bot.RandomProfiles; n > 0 {
		if members, err := b.deps.DB.Members(j.chatID); err == nil {
			rand.Shuffle(len(members), func(a, c int) { members[a], members[c] = members[c], members[a] })
			for _, m := range members {
				if n == 0 {
					break
				}
				if !seen[m.UserID()] && memory.Render(m.Profile) != "" {
					add(&more, m.UserID())
					n--
				}
			}
		}
	}

	budget := b.deps.Cfg.Bot.ProfileBudget
	var parts []string
	used := 0
	for i, id := range append(must, more...) {
		mem, err := b.deps.DB.GetMember(j.chatID, id)
		if err != nil || mem == nil {
			continue
		}
		r := memory.Render(mem.Profile)
		if r == "" {
			continue
		}
		part := displayName(mem) + ":\n" + r
		if i >= len(must) && used+len([]rune(part)) > budget {
			continue // a smaller profile further down may still fit
		}
		used += len([]rune(part))
		parts = append(parts, part)
	}
	return strings.Join(parts, "\n\n")
}

// aboutIDs returns the members the triggering message is about: the replied-to
// user and the mentioned members.
func (b *Bot) aboutIDs(j job) []int64 {
	if j.msg == nil {
		return nil
	}
	ids := []int64{replyUserID(j.msg)}
	ids = append(ids, b.namedIDs(j.chatID, j.msg.Text+" "+j.msg.Caption)...)
	for _, e := range j.msg.Entities {
		if e.Type == telego.EntityTypeMention {
			if mem, err := b.deps.DB.MemberByUsername(j.chatID, strings.TrimPrefix(entityText(j.msg.Text, e), "@")); err == nil {
				ids = append(ids, mem.UserID())
			}
		} else if e.Type == telego.EntityTypeTextMention && e.User != nil {
			ids = append(ids, e.User.ID)
		}
	}
	return ids
}

// namedIDs returns the members whose first name, nickname or username is
// written in the text as a word («سارا کیه؟», «به علی بگو»).
func (b *Bot) namedIDs(chatID int64, text string) []int64 {
	members, err := b.deps.DB.Members(chatID)
	if err != nil {
		return nil
	}
	norm := store.NormalizeTerm(text)
	var ids []int64
	for _, m := range members {
		if m.UserID() == b.botID {
			continue
		}
		for _, name := range []string{m.FirstName, m.Profile.Nickname, m.Username} {
			if hasName(norm, store.NormalizeTerm(name)) {
				ids = append(ids, m.UserID())
				break
			}
		}
	}
	return ids
}

// nameSuffixes may follow a name in the same word: «علیو», «سارارو», «سارا‌ست».
var nameSuffixes = []string{"", "رو", "را", "و", "ه", "ی", "م", "ت", "ش", "ست", "هست", "ها"}

// hasName reports whether the normalized text has the normalized name as a
// whole word, optionally followed by a short suffix. «علی» is not found in
// «علیرضا».
func hasName(text, name string) bool {
	if len([]rune(name)) < 2 {
		return false
	}
	for i := 0; ; {
		k := strings.Index(text[i:], name)
		if k < 0 {
			return false
		}
		at := i + k
		before, _ := utf8.DecodeLastRuneInString(text[:at])
		if at == 0 || !(unicode.IsLetter(before) || unicode.IsDigit(before)) {
			rest := text[at+len(name):]
			end := strings.IndexFunc(rest, func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) })
			if end < 0 {
				end = len(rest)
			}
			for _, suf := range nameSuffixes {
				if rest[:end] == suf {
					return true
				}
			}
		}
		i = at + 1
	}
}

// renderOrders renders the admin's orders about everyone in the conversation:
// the author, the people the message is about, the target, and every member
// who wrote in the transcript. Unlike profiles, they are always shown.
func (b *Bot) renderOrders(j job, msgs []store.Message) string {
	if !b.deps.Cfg.Features.Profiles.On() {
		return ""
	}
	seen := map[int64]bool{0: true, b.botID: true}
	var ids []int64
	add := func(id int64) {
		if !seen[id] {
			seen[id] = true
			ids = append(ids, id)
		}
	}
	add(j.authorID())
	for _, id := range b.aboutIDs(j) {
		add(id)
	}
	add(j.target)
	for _, m := range msgs {
		if !m.FromBot {
			add(m.UserID)
		}
	}
	var lines []string
	for _, id := range ids {
		mem, err := b.deps.DB.GetMember(j.chatID, id)
		if err != nil || mem == nil {
			continue
		}
		if o := memory.RenderOrders(mem.Profile); o != "" {
			lines = append(lines, "- "+displayName(mem)+": "+o)
		}
	}
	return strings.Join(lines, "\n")
}

// mockSweet are the marks of the mock-sweet style («ای جانم، فدای … بشم»).
var mockSweet = []string{"جانم", "فدای", "قربون", "عزیز دلم", "عزیزم"}

// varietyWindow is how far back the bot's own messages are checked for
// repetition.
const varietyWindow = 2 * time.Hour

// varietyNote tells the model how its recent messages began, so it doesn't
// open the same way again, and to skip the mock-sweet style if it used it
// recently: models latch onto one phrase and repeat it for days.
func (b *Bot) varietyNote(chatID int64) string {
	msgs, err := b.deps.DB.RecentMessages(chatID, 60)
	if err != nil {
		return ""
	}
	cutoff := time.Now().Add(-varietyWindow)
	var openings []string
	sweet := 0
	for i := len(msgs) - 1; i >= 0; i-- {
		m := msgs[i]
		if !m.FromBot || m.Date.Before(cutoff) || strings.HasPrefix(m.Text, "[") || strings.HasPrefix(m.Text, "/") {
			continue
		}
		for _, w := range mockSweet {
			if strings.Contains(m.Text, w) {
				sweet++
				break
			}
		}
		if len(openings) < 4 {
			if f := strings.Fields(m.Text); len(f) > 0 {
				openings = append(openings, "«"+strings.Join(f[:min(3, len(f))], " ")+"»")
			}
		}
	}
	var notes []string
	if len(openings) > 0 {
		notes = append(notes, "Your last messages began with "+strings.Join(openings, ", ")+": start differently and don't reuse their phrases.")
	}
	if sweet > 0 {
		notes = append(notes, "You used the mock-sweet style recently: don't use it now (no «جانم», «فدای», «قربون», «عزیزم»).")
	}
	return strings.Join(notes, " ")
}

// groupWords show a message asks about the group itself.
var groupWords = []string{"گروه", "چه خبر", "این هفته", "اخیرا", "این چند روز", "دیروز چی", "امروز چی"}

// asksAboutGroup reports whether the triggering message asks about the group,
// so the group memory is shown regardless of prompt_chance.
func asksAboutGroup(j job) bool {
	if j.msg == nil {
		return false
	}
	text := store.NormalizeTerm(j.msg.Text + " " + j.msg.Caption)
	for _, w := range groupWords {
		if strings.Contains(text, store.NormalizeTerm(w)) {
			return true
		}
	}
	return false
}

// renderGroupMemory renders the group's top events, but only with probability
// features.group_memory.prompt_chance, so the bot doesn't keep steering back to them.
func (b *Bot) renderGroupMemory(chatID int64, j job) string {
	g := b.deps.Cfg.Features.GroupMemory
	if !g.On() || g.InPrompt == 0 || (!asksAboutGroup(j) && b.roll() >= g.PromptChance) {
		return ""
	}
	events, err := b.deps.DB.GroupEvents(chatID, g.TTL)
	if err != nil {
		return ""
	}
	return memory.RenderGroup(events, g.InPrompt, time.Now())
}

// renderGlossary renders the group's terms that come up in the transcript (at
// most features.glossary.in_prompt, the longest first) and marks them used, which is
// what keeps a term from being evicted.
func (b *Bot) renderGlossary(chatID int64, transcript string) string {
	g := b.deps.Cfg.Features.Glossary
	if !g.On() {
		return ""
	}
	terms, err := b.deps.DB.Terms(chatID)
	if err != nil || len(terms) == 0 {
		return ""
	}
	text := store.NormalizeTerm(transcript)
	var hits []store.Term
	for _, t := range terms {
		if termMatches(text, t.Norm) {
			hits = append(hits, t)
		}
	}
	sort.SliceStable(hits, func(i, k int) bool { return len(hits[i].Norm) > len(hits[k].Norm) })
	if len(hits) > g.InPrompt {
		hits = hits[:g.InPrompt]
	}
	var sb strings.Builder
	norms := make([]string, 0, len(hits))
	for _, t := range hits {
		fmt.Fprintf(&sb, "- «%s»: %s\n", t.Term, t.Meaning)
		norms = append(norms, t.Norm)
	}
	_ = b.deps.DB.TouchTerms(chatID, norms)
	return strings.TrimSpace(sb.String())
}

// termMatches reports whether a term comes up in the normalized text. A verb
// given as an infinitive («چاییدن», «حال کردن») also matches its past-stem
// forms, with or without ن / می / نمی in front: «چاییدم», «نچاییدی»,
// «می‌چاییدن», «حال کردیم». (Present-stem forms are irregular and don't match.)
func termMatches(text, norm string) bool {
	if containsTerm(text, norm) {
		return true
	}
	head, last := "", norm
	if i := strings.LastIndexByte(norm, ' '); i >= 0 {
		head, last = norm[:i+1], norm[i+1:]
	}
	r := []rune(last)
	if len(r) < 4 || r[len(r)-1] != 'ن' || (r[len(r)-2] != 'د' && r[len(r)-2] != 'ت') {
		return false
	}
	stem := string(r[:len(r)-1])
	for _, prefix := range []string{"", "ن", "می ", "نمی ", "می", "نمی"} {
		if containsTerm(text, head+prefix+stem) {
			return true
		}
	}
	return false
}

// containsTerm reports whether the normalized text has the normalized term
// starting at a word boundary (Persian suffixes after it are fine: «قلقلی‌ها»).
func containsTerm(text, term string) bool {
	if term == "" {
		return false
	}
	for i := 0; ; {
		k := strings.Index(text[i:], term)
		if k < 0 {
			return false
		}
		at := i + k
		if at == 0 {
			return true
		}
		r, _ := utf8.DecodeLastRuneInString(text[:at])
		if !unicode.IsLetter(r) && !unicode.IsDigit(r) {
			return true
		}
		i = at + 1
	}
}

// reactionMarker matches the transcript's reaction note, which models copy
// into their replies: "{you reacted 😂}", "{reacted 😂}", "(you reacted 😂)".
var reactionMarker = regexp.MustCompile(`[{(]\s*(?:you\s+)?reacted\s+([^})]{1,16}?)\s*[})]`)

// emojiAliases maps common emojis that are not allowed reactions to close ones.
var emojiAliases = map[string]string{"😂": "🤣", "😆": "😁", "😄": "😁", "😅": "😁", "🙂": "👍", "😊": "🥰", "💪": "👍", "😮": "😱", "😳": "😱"}

// emojiArgs matches react's arguments written out as text: {"emoji": "🤨"}.
var emojiArgs = regexp.MustCompile(`\{[^{}]*"emoji"\s*:\s*"([^"]{1,16})"[^{}]*\}`)

// splitReactionMarkers removes what the model wrote instead of calling react
// (the transcript's "{you reacted 😂}" note, or react's JSON arguments) and
// returns the allowed emojis they named. Any other line that is only a JSON
// object (another tool's arguments written as text) is dropped too.
func splitReactionMarkers(s string) (string, []string) {
	var emojis []string
	add := func(e string) {
		e = strings.TrimSpace(strings.ReplaceAll(e, "\ufe0f", ""))
		if a, ok := emojiAliases[e]; ok {
			e = a
		}
		if r, ok := reactionEmoji(e); ok {
			emojis = append(emojis, r)
		}
	}
	for _, m := range reactionMarker.FindAllStringSubmatch(s, -1) {
		add(m[1])
	}
	s = reactionMarker.ReplaceAllString(s, "")
	for _, m := range emojiArgs.FindAllStringSubmatch(s, -1) {
		add(m[1])
	}
	s = emojiArgs.ReplaceAllString(s, "")
	var kept []string
	for _, line := range strings.Split(s, "\n") {
		t := strings.TrimSpace(line)
		if strings.HasPrefix(t, "{") && strings.HasSuffix(t, "}") && json.Valid([]byte(t)) {
			continue // tool arguments written as text: never send them
		}
		kept = append(kept, line)
	}
	return strings.Join(kept, "\n"), emojis
}

// cleanReply cleans the model output: strip thinking, a leading bot name, and
// treat "-" as an empty reply.
func cleanReply(content, botName string) string {
	s := stripToolMarkup(llm.Clean(content))
	prefixes := []string{
		"**" + botName + "**:",
		botName + ":",
	}
	for _, p := range prefixes {
		if strings.HasPrefix(s, p) {
			s = s[len(p):]
			break
		}
	}
	s = strings.TrimSpace(s)
	// "-" means silence; a "-" line next to text is a leftover of it.
	s = strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(s, "-\n"), "\n-"))
	if s == "-" || leakedReasoning(s) {
		return ""
	}
	return s
}

// leakedReasoning reports whether a reply is mostly English: the model's
// thinking that leaked into the content (Gemma sometimes writes a long English
// monologue instead of calling a tool). Persian replies with a few English
// names (a song, a band) are kept.
func leakedReasoning(s string) bool {
	latin, letters := 0, 0
	for _, r := range s {
		if !unicode.IsLetter(r) {
			continue
		}
		letters++
		if r < 0x250 {
			latin++
		}
	}
	return latin > 80 && latin*2 > letters
}

// toolMarkers start a tool call that a model wrote as text instead of as a
// real tool call (Gemma 4 does this: "<|tool_call>call:edit_memory{...}").
var toolMarkers = []string{"<|tool_call>", "<tool_call>", "<|tool_call|>"}

// stripToolMarkup drops everything from the first leaked tool-call marker on;
// such text must never reach the chat.
func stripToolMarkup(s string) string {
	for _, m := range toolMarkers {
		if i := strings.Index(s, m); i >= 0 {
			s = s[:i]
		}
	}
	return strings.TrimSpace(s)
}
