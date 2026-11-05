package memory

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/ehsanranjbar/hooshang/internal/i18n"
	"github.com/ehsanranjbar/hooshang/internal/llm"
	"github.com/ehsanranjbar/hooshang/internal/prompts"
	"github.com/ehsanranjbar/hooshang/internal/store"
)

// AnalysisConfig says when the model writes its read of a member
// (features.profiles.analysis). Every 0 turns it off.
type AnalysisConfig struct {
	Every    int // rewrite after this many more of the member's messages
	MinMsgs  int // messages a member needs before the first one
	MaxWords int // length asked of the model
}

// analysisMessages is how many of the member's own messages the analysis sees.
const analysisMessages = 60

// analysisStickers is how many of their most-sent stickers it sees.
const analysisStickers = 3

var analysisSchema = json.RawMessage(`{"type":"object","properties":{"analysis":{"type":"string"}},"required":["analysis"]}`)

// analysisDue reports whether a member's analysis should be (re)written:
// enough messages counted, and enough new ones since the last analysis.
func (u *Updater) analysisDue(m *store.Member, mem store.Memory) bool {
	a := u.cfg.Analysis
	if a.Every <= 0 || m.Style.Msgs < a.MinMsgs {
		return false
	}
	return mem.AnalysisAt.IsZero() || m.Style.Msgs-mem.AnalysisMsgs >= a.Every
}

// AnalyseNow writes the model's read of a member from everything known about
// them: their memory, style stats, most-sent stickers and their own messages
// (the archive when retrieval is on, else the recent messages). It is a
// one-call job; a failed call keeps the previous analysis.
func (u *Updater) AnalyseNow(ctx context.Context, chatID, userID int64) error {
	m, err := u.db.GetMember(chatID, userID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil
		}
		return err
	}
	mem, err := u.db.GetMemory(chatID, userID)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return err
	}
	msgs, err := u.memberMessages(chatID, userID)
	if err != nil {
		return err
	}
	if len(msgs) == 0 {
		return nil
	}
	names := map[int64]string{}
	if members, err := u.db.Members(chatID); err == nil {
		for _, x := range members {
			names[x.UserID()] = x.FirstName
		}
	}
	rendered := mem
	rendered.Analysis = "" // the previous read goes in its own section
	prompt, err := prompts.MemberAnalysis(prompts.AnalysisData{
		Language: u.cfg.Language,
		Name:     m.FirstName,
		Memory:   Render(rendered),
		Style:    RenderStyle(m.Style, names),
		Stickers: u.stickerLines(m.Style),
		Messages: strings.Join(msgs, "\n"),
		Previous: mem.Analysis,
		MaxWords: max(u.cfg.Analysis.MaxWords, 20),
	})
	if err != nil {
		return err
	}
	req := llm.Request{Model: u.model, Messages: []llm.Message{{Role: "user", Content: prompt}}, Temperature: 0.4, MaxTokens: max(u.cfg.MaxTokens, 2048)}
	if u.cfg.JSONSchema {
		req.Schema, req.SchemaName = analysisSchema, "analysis"
	}
	resp, err := u.c.Chat(ctx, req)
	if err != nil {
		return fmt.Errorf("llm: %w", err)
	}
	text, err := ParseAnalysis(resp.Content)
	if err != nil {
		return err
	}
	if n := u.cfg.Analysis.MaxWords * 10; n > 0 && len([]rune(text)) > n { // a runaway answer
		text = string([]rune(text)[:n])
	}
	msgsAt := m.Style.Msgs
	_, err = u.db.UpdateMemory(chatID, userID, func(mm *store.Memory) {
		mm.Analysis, mm.AnalysisAt, mm.AnalysisMsgs = text, time.Now(), msgsAt
	})
	return err
}

// AnalyseNowIfDue runs AnalyseNow when the member's analysis is due.
func (u *Updater) AnalyseNowIfDue(ctx context.Context, chatID, userID int64) error {
	m, err := u.db.GetMember(chatID, userID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil
		}
		return err
	}
	mem, _ := u.db.GetMemory(chatID, userID)
	if !u.analysisDue(m, mem) {
		return nil
	}
	return u.AnalyseNow(ctx, chatID, userID)
}

// ParseAnalysis reads {"analysis": "..."}, or takes a plain answer as is.
func ParseAnalysis(content string) (string, error) {
	var out struct {
		Analysis string `json:"analysis"`
	}
	if raw, err := llm.ExtractJSON(content); err == nil && json.Unmarshal(raw, &out) == nil && strings.TrimSpace(out.Analysis) != "" {
		return strings.TrimSpace(out.Analysis), nil
	}
	s := strings.TrimSpace(llm.Clean(content))
	if s == "" || strings.HasPrefix(s, "{") {
		return "", errors.New("parse: no analysis in the answer")
	}
	return s, nil
}

// memberMessages lists a member's own messages for the analysis, oldest
// first: from the archive when it has any, else the recent messages.
func (u *Updater) memberMessages(chatID, userID int64) ([]string, error) {
	now := time.Now()
	var lines []string
	arch, err := u.db.ArchivedBy(chatID, userID, analysisMessages)
	if err != nil {
		return nil, err
	}
	for _, a := range arch {
		lines = append(lines, fmt.Sprintf("(%s) %s", i18n.When(a.Date, now), a.Text))
	}
	if len(lines) > 0 {
		return lines, nil
	}
	recent, err := u.db.RecentMessages(chatID, u.cfg.ContextMessages)
	if err != nil {
		return nil, err
	}
	for _, msg := range recent {
		if msg.UserID == userID && !msg.FromBot && !strings.HasPrefix(msg.Text, "/") {
			lines = append(lines, fmt.Sprintf("(%s) %s", i18n.When(msg.Date, now), msg.Text))
		}
	}
	if len(lines) > analysisMessages {
		lines = lines[len(lines)-analysisMessages:]
	}
	return lines, nil
}

// stickerLines describes a member's most-sent stickers and GIFs.
func (u *Updater) stickerLines(s store.Style) string {
	ids := make([]string, 0, len(s.Media))
	for id := range s.Media {
		ids = append(ids, id)
	}
	sortByCount(ids, s.Media)
	var lines []string
	for _, id := range ids {
		d, err := u.db.GetMediaDesc(id)
		if err != nil || d.Text == "" {
			continue
		}
		lines = append(lines, fmt.Sprintf("- %s %s (×%d)", d.Emoji, d.Text, s.Media[id]))
		if len(lines) >= analysisStickers {
			break
		}
	}
	return strings.Join(lines, "\n")
}
