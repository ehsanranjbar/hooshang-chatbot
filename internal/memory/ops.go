package memory

import (
	"fmt"
	"strings"

	"github.com/ehsanranjbar/hooshang/internal/store"
)

// Op is one memory edit requested by the admin through the edit_memory tool.
// Action is add | remove | replace.
type Op struct {
	Action  string `json:"action"`
	Section string `json:"section"`
	Text    string `json:"text"`
	OldText string `json:"old_text"`
}

// Apply applies the ops to the profile in order and returns the result plus
// a human-readable change list. An unknown section or action, or a remove
// that matches nothing, returns an error naming it.
func Apply(p store.Profile, ops []Op) (store.Profile, []string, error) {
	out := p
	var changes []string
	for i, op := range ops {
		section := strings.ToLower(strings.TrimSpace(op.Section))
		action := strings.ToLower(strings.TrimSpace(op.Action))
		switch action {
		case "add":
			if err := opAdd(&out, section, op.Text); err != nil {
				return out, changes, fmt.Errorf("op %d (add): %w", i+1, err)
			}
			changes = append(changes, "add "+section+": "+op.Text)
		case "remove":
			target := op.OldText
			if target == "" {
				target = op.Text
			}
			removed, err := opRemove(&out, section, target)
			if err != nil {
				return out, changes, fmt.Errorf("op %d (remove): %w", i+1, err)
			}
			for _, r := range removed {
				changes = append(changes, "remove "+section+": "+r)
			}
		case "replace":
			if (section == "nickname" || section == "summary") && strings.TrimSpace(op.OldText) == "" {
				if err := opAdd(&out, section, op.Text); err != nil {
					return out, changes, fmt.Errorf("op %d (replace): %w", i+1, err)
				}
				changes = append(changes, "set "+section+": "+op.Text)
				continue
			}
			removed, err := opRemove(&out, section, op.OldText)
			if err != nil {
				return out, changes, fmt.Errorf("op %d (replace): %w", i+1, err)
			}
			if err := opAdd(&out, section, op.Text); err != nil {
				return out, changes, fmt.Errorf("op %d (replace): %w", i+1, err)
			}
			for _, r := range removed {
				changes = append(changes, "replace "+section+": "+r+" → "+op.Text)
			}
		default:
			return out, changes, fmt.Errorf("unknown action %q (allowed: add, remove, replace)", op.Action)
		}
	}
	return out, changes, nil
}

// opAdd appends {Text, Locked:true} to a section; for nickname/summary it
// sets the string.
func opAdd(p *store.Profile, section, text string) error {
	text = strings.TrimSpace(text)
	if text == "" {
		return fmt.Errorf("empty text for section %q", section)
	}
	switch section {
	case "nickname":
		p.Nickname, p.NicknameLocked = text, true
	case "summary":
		p.Summary, p.SummaryLocked = text, true
	default:
		items := itemsOf(p, section)
		if items == nil {
			return fmt.Errorf("unknown section %q", section)
		}
		*items = append(*items, store.Item{Text: text, Locked: true})
	}
	return nil
}

// opRemove deletes items whose normalized text contains the normalized
// target, and records the removed texts in Suppressed. It returns the
// removed texts; an error if nothing matched.
func opRemove(p *store.Profile, section, target string) ([]string, error) {
	target = strings.TrimSpace(target)
	if target == "" {
		return nil, fmt.Errorf("empty old_text for %q", section)
	}
	nt := Normalize(target)
	switch section {
	case "nickname":
		if Normalize(p.Nickname) != nt {
			return nil, fmt.Errorf("no nickname matches %q", target)
		}
		suppress(p, p.Nickname)
		p.Nickname, p.NicknameLocked = "", false
		return []string{target}, nil
	case "summary":
		if Normalize(p.Summary) != nt {
			return nil, fmt.Errorf("no summary matches %q", target)
		}
		suppress(p, p.Summary)
		p.Summary, p.SummaryLocked = "", false
		return []string{target}, nil
	default:
		items := itemsOf(p, section)
		if items == nil {
			return nil, fmt.Errorf("unknown section %q", section)
		}
		var kept, removed []store.Item
		for _, it := range *items {
			if strings.Contains(Normalize(it.Text), nt) {
				removed = append(removed, it)
			} else {
				kept = append(kept, it)
			}
		}
		if len(removed) == 0 {
			return nil, fmt.Errorf("no %s matches %q", section, target)
		}
		for _, it := range removed {
			suppress(p, it.Text)
		}
		*items = kept
		texts := make([]string, len(removed))
		for i, it := range removed {
			texts[i] = it.Text
		}
		return texts, nil
	}
}

// suppress adds text to Suppressed if it is not already there.
func suppress(p *store.Profile, text string) {
	for _, s := range p.Suppressed {
		if Normalize(s) == Normalize(text) {
			return
		}
	}
	p.Suppressed = append(p.Suppressed, text)
}
