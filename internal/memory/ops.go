package memory

import (
	"fmt"
	"strings"
	"time"

	"github.com/ehsanranjbar/hooshang/internal/store"
)

// Op is one memory edit requested by the admin through the edit_memory tool.
// Action is add | remove | replace; Section is a kind (fact, order, …),
// "nickname" or "summary"; ItemID names an item exactly (optional).
type Op struct {
	Action  string `json:"action"`
	Section string `json:"section"`
	Text    string `json:"text"`
	OldText string `json:"old_text"`
	ItemID  uint64 `json:"item_id"`
}

// Apply applies the ops to the memory in place, in order, and returns a
// human-readable change list. An unknown section or action, or a remove that
// matches nothing, returns an error naming it (the memory is then half
// changed; callers apply inside a transaction they can abandon).
func Apply(m *store.Memory, ops []Op, now time.Time) ([]string, error) {
	var changes []string
	for i, op := range ops {
		section := strings.ToLower(strings.TrimSpace(op.Section))
		action := strings.ToLower(strings.TrimSpace(op.Action))
		if !knownSection(section) {
			return changes, fmt.Errorf("op %d: unknown section %q (allowed: %s)", i+1, op.Section, strings.Join(Sections(), ", "))
		}
		switch action {
		case "add":
			if err := opAdd(m, section, op.Text, now); err != nil {
				return changes, fmt.Errorf("op %d (add): %w", i+1, err)
			}
			changes = append(changes, "add "+section+": "+op.Text)
		case "remove":
			target := op.OldText
			if target == "" {
				target = op.Text
			}
			removed, err := opRemove(m, section, target, op.ItemID)
			if err != nil {
				return changes, fmt.Errorf("op %d (remove): %w", i+1, err)
			}
			for _, r := range removed {
				changes = append(changes, "remove "+section+": "+r)
			}
		case "replace":
			if (section == "nickname" || section == "summary") && strings.TrimSpace(op.OldText) == "" {
				if err := opAdd(m, section, op.Text, now); err != nil {
					return changes, fmt.Errorf("op %d (replace): %w", i+1, err)
				}
				changes = append(changes, "set "+section+": "+op.Text)
				continue
			}
			removed, err := opRemove(m, section, op.OldText, op.ItemID)
			if err != nil {
				return changes, fmt.Errorf("op %d (replace): %w", i+1, err)
			}
			if err := opAdd(m, section, op.Text, now); err != nil {
				return changes, fmt.Errorf("op %d (replace): %w", i+1, err)
			}
			for _, r := range removed {
				changes = append(changes, "replace "+section+": "+r+" → "+op.Text)
			}
		default:
			return changes, fmt.Errorf("op %d: unknown action %q (allowed: add, remove, replace)", i+1, op.Action)
		}
	}
	return changes, nil
}

// knownSection reports whether edit_memory accepts the section.
func knownSection(s string) bool {
	for _, k := range Sections() {
		if k == s {
			return true
		}
	}
	return false
}

// opAdd adds a locked item of the kind; for nickname/summary it sets and
// locks the string.
func opAdd(m *store.Memory, section, textT string, now time.Time) error {
	textT = strings.TrimSpace(textT)
	if textT == "" {
		return fmt.Errorf("empty text for section %q", section)
	}
	switch section {
	case "nickname":
		m.Nickname, m.NicknameLocked = textT, true
	case "summary":
		m.Summary, m.SummaryLocked = textT, true
	default:
		if cur := find(m, section, textT); cur != nil {
			cur.Locked, cur.Seen, cur.LastAt, cur.Expires = true, cur.Seen+1, now, time.Time{}
			return nil
		}
		m.Add(store.Item{Kind: section, Text: textT, Locked: true, Seen: 2, FirstAt: now, LastAt: now})
	}
	return nil
}

// opRemove deletes the item with the id, or the items of the kind whose
// folded text contains the folded target, and suppresses the removed texts so
// the extractor does not bring them back. It returns the removed texts; an
// error if nothing matched.
func opRemove(m *store.Memory, section, target string, id uint64) ([]string, error) {
	target = strings.TrimSpace(target)
	switch section {
	case "nickname":
		if m.Nickname == "" || (target != "" && Normalize(m.Nickname) != Normalize(target)) {
			return nil, fmt.Errorf("no nickname matches %q", target)
		}
		suppress(m, m.Nickname)
		old := m.Nickname
		m.Nickname, m.NicknameLocked = "", false
		return []string{old}, nil
	case "summary":
		if m.Summary == "" || (target != "" && Normalize(m.Summary) != Normalize(target)) {
			return nil, fmt.Errorf("no summary matches %q", target)
		}
		suppress(m, m.Summary)
		old := m.Summary
		m.Summary, m.SummaryLocked = "", false
		return []string{old}, nil
	}
	if id != 0 {
		it := m.Find(id)
		if it == nil || it.Kind != section {
			return nil, fmt.Errorf("no %s item with id %d", section, id)
		}
		textT := it.Text
		suppress(m, textT)
		m.Remove(id)
		return []string{textT}, nil
	}
	if target == "" {
		return nil, fmt.Errorf("empty old_text for %q", section)
	}
	nt := Normalize(target)
	var removed []string
	for _, it := range m.Kind(section) {
		if strings.Contains(Normalize(it.Text), nt) {
			removed = append(removed, it.Text)
			suppress(m, it.Text)
			m.Remove(it.ID)
		}
	}
	if len(removed) == 0 {
		return nil, fmt.Errorf("no %s matches %q", section, target)
	}
	return removed, nil
}

// suppress adds text to Suppressed if it is not already there.
func suppress(m *store.Memory, textT string) {
	for _, s := range m.Suppressed {
		if Normalize(s) == Normalize(textT) {
			return
		}
	}
	m.Suppressed = append(m.Suppressed, textT)
}
