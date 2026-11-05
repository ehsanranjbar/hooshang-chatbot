package bot

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/ehsanranjbar/hooshang/internal/text"
	"strconv"
	"strings"

	"github.com/ehsanranjbar/hooshang/internal/ganjoor"
	"github.com/ehsanranjbar/hooshang/internal/i18n"
	"github.com/ehsanranjbar/hooshang/internal/llm"
	"github.com/ehsanranjbar/hooshang/internal/memory"
	"github.com/ehsanranjbar/hooshang/internal/store"
)

// toolsFor returns the tool definitions and handlers for a job. The admin-only
// tools (get_memory, edit_memory, moderate, set_moderation) are included only when the author is an admin.
// When tools are disabled in config, no tools are returned.
func (b *Bot) toolsFor(j job) ([]llm.Tool, map[string]llm.ToolFunc) {
	if !b.deps.Cfg.LLM.Tools {
		return nil, nil
	}
	var tools []llm.Tool
	handlers := map[string]llm.ToolFunc{}
	if b.deps.Cfg.Features.Faal.On() {
		tools = append(tools, hafezFaalTool())
		handlers["hafez_faal"] = b.toolFaal(j)
	}
	if b.deps.Cfg.Features.Poem.On() {
		tools = append(tools, randomPoemTool())
		handlers["random_poem"] = b.toolPoem(j)
	}
	if j.msg != nil {
		tools = append(tools, reactTool())
		handlers["react"] = b.toolReact(j)
	}
	if m := b.deps.Cfg.Features.Media; m.On() && m.Send && (m.Stickers || m.GIFs) {
		tools = append(tools, mediaTools()...)
		handlers["find_media"] = b.toolFindMedia(j)
		handlers["send_media"] = b.toolSendMedia(j)
	}
	if b.deps.Cfg.Features.Glossary.On() && j.authorID() > 0 {
		tools = append(tools,
			llm.Tool{Name: "learn_term", Description: "Save a local word, phrase or inside joke (اصطلاح) of this group and what it means. Call it ONLY when someone explicitly explains or corrects what a term means (e.g. «\"چاییدن\" یعنی سرما خوردن», «به این میگیم ...»). Not for facts about a person, and not for normal words everyone knows.",
				Parameters: json.RawMessage(`{"type":"object","properties":{"term":{"type":"string","description":"the term, as written in the chat"},"meaning":{"type":"string","description":"what it means, one short Persian line"}},"required":["term","meaning"]}`)},
			llm.Tool{Name: "forget_term", Description: "Forget a term of the group's glossary. Only when someone explicitly asks to forget it.",
				Parameters: json.RawMessage(`{"type":"object","properties":{"term":{"type":"string"}},"required":["term"]}`)},
		)
		handlers["learn_term"] = b.toolLearnTerm(j)
		handlers["forget_term"] = b.toolForgetTerm(j)
	}
	if b.deps.Cfg.Features.Profiles.On() && j.authorID() > 0 && b.deps.Admins.IsAdmin(j.chatID, j.authorID()) {
		tools = append(tools,
			llm.Tool{Name: "get_memory", Description: "Show what is remembered about a member.",
				Parameters: json.RawMessage(`{"type":"object","properties":{"member":{"type":"string"}},"required":["member"]}`)},
			llm.Tool{Name: "edit_memory", Description: "Add, remove or replace remembered items for a member. Only for explicit requests.",
				Parameters: json.RawMessage(`{"type":"object","properties":{"member":{"type":"string"},"ops":{"type":"array","items":{"type":"object","properties":{"action":{"type":"string","enum":["add","remove","replace"]},"section":{"type":"string","enum":["nickname","summary","facts","interests","quirks","relationships","boundaries","orders"]},"text":{"type":"string"},"old_text":{"type":"string"}},"required":["action","section"]}}},"required":["member","ops"]}`)},
		)
		handlers["get_memory"] = b.toolGetMemory(j)
		handlers["edit_memory"] = b.toolEditMemory(j)
	}
	if b.deps.Cfg.Features.Moderation.On() && j.authorID() > 0 && b.deps.Admins.IsAdmin(j.chatID, j.authorID()) {
		tools = append(tools, moderationTools()...)
		handlers["moderate"] = b.toolModerate(j)
		handlers["set_moderation"] = b.toolSetModeration(j)
	}
	return tools, handlers
}

// hafezFaalTool is the hafez_faal tool definition.
func hafezFaalTool() llm.Tool {
	return llm.Tool{Name: "hafez_faal", Description: "Draw a Hafez faal (a random ghazal, its meaning and an interpretation are sent to the chat). Call it ONLY when someone explicitly asks for a faal (فال، فال حافظ); never offer or draw one on your own. Optional wish (نیت) in Persian.",
		Parameters: json.RawMessage(`{"type":"object","properties":{"wish":{"type":"string","description":"the optional wish (نیت)"}}}`)}
}

// randomPoemTool is the random_poem tool definition.
func randomPoemTool() llm.Tool {
	return llm.Tool{Name: "random_poem", Description: "Send a random poem by a Persian poet to the chat. Call it ONLY when someone explicitly asks for a poem (شعر); never on your own. Optional poet name.",
		Parameters: json.RawMessage(`{"type":"object","properties":{"poet":{"type":"string","description":"poet name, e.g. حافظ; empty for a random poet"}}}`)}
}

// toolFaal is the hafez_faal tool handler.
func (b *Bot) toolFaal(j job) llm.ToolFunc {
	return func(ctx context.Context, args string) (string, error) {
		var a struct{ Wish string }
		if err := json.Unmarshal([]byte(args), &a); err != nil {
			return "error: bad arguments", nil
		}
		if !askedFor(j, faalWords) {
			return notAsked, nil
		}
		replyTo := 0
		if j.msg != nil {
			replyTo = j.msg.MessageID
		}
		// The faal is 2 messages (the card, the تعبیر); runReply's slot pays
		// for one of them since `answered` drops the model's own reply.
		if !b.deps.Limiter.Reserve(j.chatID, 1) {
			return i18n.RateLimited, nil
		}
		title := b.runFaal(ctx, j.chatID, replyTo, j.authorID(), strings.TrimSpace(a.Wish))
		if j.answered != nil {
			*j.answered = true // the poem (or the Ganjoor error) is the whole answer
		}
		if title == "" {
			return "error: the faal failed and the user was already told; say nothing more", nil
		}
		return fmt.Sprintf(i18n.FaalSentNote, title), nil
	}
}

// Words that show a message asks for a faal or a poem. Models (Gemma) call the
// tools for unrelated questions too, so the handlers check the message itself.
var (
	faalWords = []string{"فال", "نیت", "faal"}
	poemWords = []string{"شعر", "بیت", "غزل", "دوبیتی", "رباعی", "قصیده", "مثنوی", "poem"}
)

// notAsked is the tool result when nobody asked for it.
const notAsked = "error: nobody asked for this; do not mention it, just answer the message normally"

// askedFor reports whether the triggering message, or the message it replies
// to when that is the bot's own poem or faal, has one of the words.
func askedFor(j job, words []string) bool {
	if j.msg == nil {
		return false
	}
	texts := []string{j.msg.Text, j.msg.Caption}
	if r := j.msg.ReplyToMessage; r != nil && r.From != nil && r.From.IsBot {
		texts = append(texts, r.Text, r.Caption)
	}
	for _, t := range texts {
		norm := normalizeName(t)
		for _, w := range words {
			if w != "" && strings.Contains(norm, normalizeName(w)) {
				return true
			}
		}
	}
	return false
}

// toolPoem is the random_poem tool handler.
func (b *Bot) toolPoem(j job) llm.ToolFunc {
	return func(ctx context.Context, args string) (string, error) {
		var a struct{ Poet string }
		if err := json.Unmarshal([]byte(args), &a); err != nil {
			return "error: bad arguments", nil
		}
		if !askedFor(j, append(poemWords, ganjoor.PoetNames()...)) {
			return notAsked, nil
		}
		poetID, ok := poetFor(a.Poet)
		if !ok {
			return "error: unknown poet; known poets: " + strings.Join(ganjoor.PoetNames(), ", "), nil
		}
		poem, err := b.deps.Ganjoor.RandomPoem(ctx, poetID)
		if err != nil {
			return "error: ganjoor is unavailable; tell the user briefly", nil
		}
		// One message, paid for by runReply's slot (`answered` drops the reply).
		replyTo := 0
		if j.msg != nil {
			replyTo = j.msg.MessageID
		}
		_ = b.sendPoem(ctx, j.chatID, replyTo, poem)
		if j.answered != nil {
			*j.answered = true // the poem is the whole answer; drop the model's follow-up
		}
		return fmt.Sprintf(i18n.PoemSentNote, poem.FullTitle), nil
	}
}

// Limits of a glossary entry, in runes.
const (
	maxTermRunes    = 40
	maxMeaningRunes = 200
)

// toolLearnTerm is the learn_term handler: anyone may teach or correct a term.
func (b *Bot) toolLearnTerm(j job) llm.ToolFunc {
	return func(_ context.Context, args string) (string, error) {
		var a struct {
			Term    string `json:"term"`
			Meaning string `json:"meaning"`
		}
		if err := json.Unmarshal([]byte(args), &a); err != nil {
			return "error: bad arguments", nil
		}
		a.Term = strings.Trim(strings.TrimSpace(a.Term), "\"'«»“”")
		a.Meaning = strings.TrimSpace(a.Meaning)
		switch {
		case a.Term == "" || a.Meaning == "":
			return "error: both term and meaning are needed", nil
		case len([]rune(a.Term)) > maxTermRunes:
			return "error: that is too long for a term", nil
		case len([]rune(a.Meaning)) > maxMeaningRunes:
			a.Meaning = string([]rune(a.Meaning)[:maxMeaningRunes])
		}
		name := ""
		if j.msg != nil {
			name = displayNameOf(j.msg)
		}
		_, err := b.deps.DB.PutTerm(j.chatID, store.Term{
			Term: a.Term, Meaning: a.Meaning, ByID: j.authorID(), ByName: name,
		}, b.deps.Cfg.Features.Glossary.MaxItems)
		if err != nil {
			return "error: " + err.Error(), nil
		}
		return fmt.Sprintf("saved: «%s» = %s", a.Term, a.Meaning), nil
	}
}

// toolForgetTerm is the forget_term handler: the admin or whoever taught the
// term may remove it.
func (b *Bot) toolForgetTerm(j job) llm.ToolFunc {
	return func(_ context.Context, args string) (string, error) {
		var a struct {
			Term string `json:"term"`
		}
		if err := json.Unmarshal([]byte(args), &a); err != nil {
			return "error: bad arguments", nil
		}
		t, err := b.deps.DB.GetTerm(j.chatID, strings.Trim(strings.TrimSpace(a.Term), "\"'«»“”"))
		if err != nil {
			return "error: no such term in the glossary", nil
		}
		if t.ByID != j.authorID() && !b.deps.Admins.IsAdmin(j.chatID, j.authorID()) {
			return "error: only the admin or " + t.ByName + " (who taught it) can remove it", nil
		}
		if err := b.deps.DB.DeleteTerm(j.chatID, t.Term); err != nil {
			return "error: " + err.Error(), nil
		}
		return "forgotten: «" + t.Term + "»", nil
	}
}

// toolGetMemory is the get_memory admin tool handler.
func (b *Bot) toolGetMemory(j job) llm.ToolFunc {
	return func(_ context.Context, args string) (string, error) {
		var a struct{ Member string }
		if err := json.Unmarshal([]byte(args), &a); err != nil {
			return "error: bad arguments", nil
		}
		id, err := b.resolveMember(j.chatID, a.Member, j)
		if err != nil {
			return err.Error(), nil
		}
		mem, err := b.deps.DB.GetMember(j.chatID, id)
		if err != nil || mem == nil {
			return "error: no such member", nil
		}
		return renderMemory(mem), nil
	}
}

// toolEditMemory is the edit_memory admin tool handler.
func (b *Bot) toolEditMemory(j job) llm.ToolFunc {
	return func(_ context.Context, args string) (string, error) {
		var a struct {
			Member string      `json:"member"`
			Ops    []memory.Op `json:"ops"`
		}
		if err := json.Unmarshal([]byte(args), &a); err != nil {
			return "error: bad arguments", nil
		}
		id, err := b.resolveMember(j.chatID, a.Member, j)
		if err != nil {
			return err.Error(), nil
		}
		if len(a.Ops) == 0 {
			return "error: no operations given", nil
		}
		var before, after store.Profile
		var changes []string
		var applyErr error
		_, err = b.deps.DB.UpdateMember(j.chatID, id, func(m *store.Member) {
			before = m.Profile
			var p store.Profile
			p, changes, applyErr = memory.Apply(m.Profile, a.Ops)
			if applyErr != nil {
				return // nothing changes; the member is written back as it was
			}
			after = p
			m.Profile = p
		})
		if err != nil {
			return "error: " + err.Error(), nil
		}
		if applyErr != nil {
			return "error: " + applyErr.Error(), nil
		}
		// Record the edit for the audit log.
		request := ""
		if j.msg != nil {
			request = j.msg.Text
		}
		_ = b.deps.DB.AddEdit(j.chatID, &store.Edit{
			AdminID: j.authorID(),
			UserID:  id,
			Request: request,
			Ops:     args,
			Before:  before,
			After:   after,
		})
		return strings.Join(changes, "; "), nil
	}
}

// renderMemory renders a member's profile plus a numbered list of items per
// section, for the get_memory tool.
func renderMemory(m *store.Member) string {
	var sb strings.Builder
	name := m.FirstName
	if m.LastName != "" {
		name += " " + m.LastName
	}
	sb.WriteString(name + ":\n")
	sb.WriteString(memory.Render(m.Profile))
	sb.WriteByte('\n')
	for _, section := range memory.Sections() {
		items := profileItems(m.Profile, section)
		if len(items) == 0 {
			continue
		}
		sb.WriteString(section + ":\n")
		for i, it := range items {
			fmt.Fprintf(&sb, "%d. %s%s\n", i+1, it.Text, lockedMark(it.Locked))
		}
	}
	return strings.TrimSpace(sb.String())
}

// profileItems returns the item list for a section (nickname/summary are
// strings, so they yield no items here).
func profileItems(p store.Profile, section string) []store.Item {
	switch section {
	case "facts":
		return p.Facts
	case "interests":
		return p.Interests
	case "quirks":
		return p.Quirks
	case "relationships":
		return p.Relationships
	case "boundaries":
		return p.Boundaries
	case "orders":
		return p.Orders
	default:
		return nil
	}
}

// lockedMark returns a marker for locked items.
func lockedMark(locked bool) string {
	if locked {
		return " (locked)"
	}
	return ""
}

// resolveMember resolves a member reference (§6): "reply"/empty → the
// replied-to user; @username → MemberByUsername; digits → user ID; otherwise a
// normalized name match against the chat's members. Members who said
// /ignoreme never resolve (the bot does not talk about them).
func (b *Bot) resolveMember(chatID int64, s string, j job) (int64, error) {
	return b.resolveMemberOpts(chatID, s, j, false)
}

// resolveModerationTarget is resolveMember for moderation: an ignored member
// resolves too, since being ignored must not shield anyone from the admin.
func (b *Bot) resolveModerationTarget(chatID int64, s string, j job) (int64, error) {
	return b.resolveMemberOpts(chatID, s, j, true)
}

// resolveMemberOpts is the shared resolver; withIgnored lets ignored members through.
func (b *Bot) resolveMemberOpts(chatID int64, s string, j job, withIgnored bool) (int64, error) {
	s = strings.TrimSpace(s)
	switch {
	case s == "" || strings.EqualFold(s, "reply"):
		if id := replyUserID(j.msg); id != 0 && (withIgnored || !b.isIgnored(chatID, id)) {
			return id, nil
		}
		return 0, fmt.Errorf("error: no member to resolve")
	case strings.HasPrefix(s, "@"):
		mem, err := b.deps.DB.MemberByUsername(chatID, s[1:])
		if err != nil || (mem.Ignored && !withIgnored) {
			return 0, fmt.Errorf("error: no member matches %s", s)
		}
		return mem.UserID(), nil
	}
	// digits → user ID, which must be a known member of this chat
	if id, err := strconv.ParseInt(s, 10, 64); err == nil {
		if mem, err := b.deps.DB.GetMember(chatID, id); err != nil || (mem.Ignored && !withIgnored) {
			return 0, fmt.Errorf("error: no member matches %s", s)
		}
		return id, nil
	}
	// Name match against Members.
	members, err := b.deps.DB.Members(chatID)
	if err != nil {
		return 0, fmt.Errorf("error: could not list members")
	}
	norm := normalizeName(s)
	var matches []int64
	for _, m := range members {
		if matchesName(m, norm) && (withIgnored || !m.Ignored) {
			matches = append(matches, m.UserID())
		}
	}
	switch len(matches) {
	case 0:
		return 0, fmt.Errorf("error: no member matches %s", s)
	case 1:
		return matches[0], nil
	default:
		var names []string
		for _, id := range matches {
			mem, _ := b.deps.DB.GetMember(chatID, id)
			label := displayName(mem)
			if mem != nil && mem.Username != "" {
				label += " (@" + mem.Username + ")"
			}
			names = append(names, label)
		}
		return 0, fmt.Errorf("error: ambiguous: %s", strings.Join(names, ", "))
	}
}

// normalizeName folds a name for matching (text.Squash).
func normalizeName(s string) string { return text.Squash(s) }

// matchesName reports whether a member matches a normalized name on first,
// last or full name.
func matchesName(m *store.Member, norm string) bool {
	if norm == "" {
		return false
	}
	candidates := []string{m.FirstName, m.LastName, m.FirstName + m.LastName}
	if m.Profile.Nickname != "" {
		candidates = append(candidates, m.Profile.Nickname)
	}
	for _, c := range candidates {
		if normalizeName(c) == norm {
			return true
		}
	}
	return false
}
