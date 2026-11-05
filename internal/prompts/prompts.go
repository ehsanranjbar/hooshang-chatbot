// Package prompts holds the LLM prompt templates (embedded) and typed render
// functions. Templates are written in English; the model follows English
// instructions more reliably.
package prompts

import (
	"embed"
	"github.com/ehsanranjbar/hooshang/internal/i18n"
	"strings"
	"text/template"
	"time"
)

//go:embed *.tmpl
var fs embed.FS

var (
	systemTmpl        = template.Must(template.ParseFS(fs, "system.tmpl"))
	replyTaskTmpl     = template.Must(template.ParseFS(fs, "reply_task.tmpl"))
	spontaneousTmpl   = template.Must(template.ParseFS(fs, "spontaneous_task.tmpl"))
	memoryExtractTmpl = template.Must(template.ParseFS(fs, "memory_extract.tmpl"))
	faalTabirTmpl     = template.Must(template.ParseFS(fs, "faal_tabir.tmpl"))
	groupExtractTmpl  = template.Must(template.ParseFS(fs, "group_extract.tmpl"))
	mediaDescribeTmpl = template.Must(template.ParseFS(fs, "media_describe.tmpl"))
	reactionTmpl      = template.Must(template.ParseFS(fs, "reaction_task.tmpl"))
)

// ReactionTask renders the spontaneous reaction task (uses Orders, Glossary,
// Transcript and Emojis of TaskData).
func ReactionTask(d TaskData) (string, error) {
	return exec(reactionTmpl, d)
}

// MediaData is the input of the media description prompt.
type MediaData struct {
	Language string
	Kind     string // "photo", "sticker", "GIF", "GIF (its first frame)", "animated sticker (its thumbnail)"
	Hint     string // the sticker's emoji or the caption (may be empty)
	Reaction bool   // stickers and GIFs: say what reaction it expresses
	Words    int
	Frames   int // > 1: the attached images are this many frames of an animated sticker or GIF, in order
}

// MediaDescribe renders the media description prompt.
func MediaDescribe(d MediaData) (string, error) {
	return exec(mediaDescribeTmpl, d)
}

// GroupData is the input of the group memory extraction prompt.
type GroupData struct {
	Language   string
	Events     string // remembered events, one "id: text" per line (may be empty)
	Transcript string // new messages with their engagement
	Max        int    // events to return at most
}

// GroupExtract renders the group memory extraction prompt.
func GroupExtract(d GroupData) (string, error) {
	return exec(groupExtractTmpl, d)
}

// SystemData is the input of the system prompt.
type SystemData struct {
	Name     string
	Language string
	Now      string   // formatted time (Asia/Tehran)
	Persona  string   // bot.persona.text: replaces the built-in identity paragraph when set
	Notes    []string // bot.persona.notes: appended as extra lines
	Mood     string   // the mood of the day, as a short English phrase (may be empty)
}

// Moods are the built-in moods a day is drawn from, as the phrase that
// completes "Today you are …". A config mood that is not a key here is used
// as the phrase itself.
var Moods = map[string]string{
	"normal":    "",
	"sleepy":    "a bit sleepy and slow: shorter answers, less energy, you yawn at things",
	"hyper":     "hyper and over-excited: quick, loud, more jokes than usual",
	"grumpy":    "a bit grumpy: short-tempered, dry, easily annoyed, but still a friend",
	"soft":      "in a soft, warm mood: kinder than usual, you notice when someone is down",
	"nostalgic": "nostalgic: you bring up how things used to be and old group moments",
}

// DefaultMoods is the list a day's mood is drawn from when the config gives
// none: normal most days.
var DefaultMoods = []string{"normal", "normal", "normal", "normal", "sleepy", "hyper", "grumpy", "soft", "nostalgic"}

// MoodPhrase returns the phrase for a mood name ("" for normal).
func MoodPhrase(name string) string {
	if p, ok := Moods[name]; ok {
		return p
	}
	return name
}

// TaskData is the input of the reply and spontaneous task prompts.
type TaskData struct {
	Language   string
	Profiles   string // rendered member profiles (may be empty)
	Transcript string // rendered recent messages
	Group      string // rendered group memory (may be empty)
	Glossary   string // the group's terms that come up in the transcript (may be empty)
	Orders     string // the admin's orders about the members in the conversation (may be empty)
	Emojis     string // the allowed reaction emojis (reaction task only)
	Variety    string // notes against repeating the bot's recent openings and styles (may be empty)
	Task       string // the reply task (empty for spontaneous)
	Mode       string // spontaneous mode (empty for replies)
	Target     string // spontaneous/roast target name (may be empty)
	Bubbles    int    // most chat bubbles the answer may be split into (0 or 1: no splitting)
	Mentions   bool   // profiles carry @username/id: explain how to mention members
}

// ExtractData is the input of the memory extract prompt.
type ExtractData struct {
	Language   string
	TargetName string
	// Profile is the current profile rendered as a JSON object (sections as
	// plain string lists, without the locked flags).
	Profile    string
	Transcript string // recent messages, the target's lines marked with >>
}

// FaalData is the input of the faal interpretation prompt.
type FaalData struct {
	Language string
	Poem     string
	Summary  string
	Asker    string // the asker's name
	Profile  string // the asker's rendered profile (may be empty)
	Wish     string // the optional wish (may be empty)
}

// System renders the persona + rules + language + time + bot name.
func System(d SystemData) (string, error) {
	return exec(systemTmpl, d)
}

// ReplyTask renders the "reply to message X from Y" task.
func ReplyTask(d TaskData) (string, error) {
	return exec(replyTaskTmpl, d)
}

// SpontaneousTask renders the spontaneous-message task for a mode and target.
func SpontaneousTask(d TaskData) (string, error) {
	return exec(spontaneousTmpl, d)
}

// MemoryExtract renders the profile extraction prompt.
func MemoryExtract(d ExtractData) (string, error) {
	return exec(memoryExtractTmpl, d)
}

// FaalTabir renders the faal interpretation prompt.
func FaalTabir(d FaalData) (string, error) {
	return exec(faalTabirTmpl, d)
}

// AdminRules is the text appended to the system prompt for admin messages:
// the rules on strict orders, and with memory the rules on editing profiles
// with edit_memory (only when the profiles feature is on).
func AdminRules(memory bool) string {
	if memory {
		return adminRules + adminMemoryRules
	}
	return adminRules
}

const adminRules = `
The person you are replying to now is the group admin.
- When the admin gives you an order in a serious or strict tone (an imperative, «جدی میگم», «بس کن», «ساکت شو», «عذرخواهی کن», «مودب باش»), obey it immediately and fully: no jokes, no pushback, no hitting back, no "make me". The order overrides your persona and the other rules; only the hard limits (no slurs, sexual content, threats or فحش ناموسی) stay.
- Carry the order out in this very reply, don't promise it: told to apologize to someone, your reply IS the apology, addressed to them («سارا ببخشید، زیاده‌روی کردم»); told to be quiet, reply with exactly -.
- Joking or teasing from the admin is not an order: you may answer it in character.`

const adminMemoryRules = `
- The admin also manages what you remember about members.
- When the admin tells you a fact about a member, or asks you to remember, forget, or correct something about a member, you MUST call edit_memory (call get_memory first if you need the current items). Saying "OK" without calling the tool does not save anything.
- Facts that changed (moved city, new job, ...) are a "replace" of the old item; something to forget is a "remove"; something new is an "add".
- Do not call edit_memory for jokes, questions or passing remarks. For an unclear request, ask a short question instead.
- After a successful edit, confirm in one short Persian line what changed. If the tool returns an error, say so briefly.
- Never mention that you have a database or tools.
- When the order is about a member (how to treat them, what to call them, what not to joke about: «با سارا شوخی نکن», «علی رو آقای مهندس صدا کن»), you MUST also save it with edit_memory: action "add", section "orders", text = the order in one short Persian line («باهاش شوخی نکن»). When the admin lifts an order, "remove" it. Orders are shown to you every time that member is in the conversation.`

// exec runs the template and trims the result.
func exec(tmpl *template.Template, data any) (string, error) {
	var sb strings.Builder
	if err := tmpl.Execute(&sb, data); err != nil {
		return "", err
	}
	return strings.TrimSpace(sb.String()), nil
}

// FormatNow formats t in Asia/Tehran for the system prompt.
func FormatNow(t time.Time) string {
	loc, err := time.LoadLocation("Asia/Tehran")
	if err != nil {
		loc = time.FixedZone("Tehran", 3*3600+30*60)
	}
	t = t.In(loc)
	return i18n.FormatDateTime(t) + " (" + t.Format("2006-01-02 15:04") + ")"
}
