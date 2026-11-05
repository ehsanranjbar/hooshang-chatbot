package llm

import (
	"encoding/json"
	"fmt"
	"strings"
)

// The model may wrap its reasoning in think tags; the tag names are built
// from unicode escapes so they never appear as literal markup in source.
const thinkOpen, thinkClose = "\u003cthink\u003e", "\u003c/think\u003e"

// Gemma 4's native reasoning channel, which some builds (the 31B MLX one in
// LM Studio) leave in the content after a tool call: «<|channel>thought …<channel|>answer».
const channelOpen, channelClose = "\u003c|channel\u003e", "\u003cchannel|\u003e"

// Clean removes reasoning blocks (think tags, Gemma's thought channel) and
// trims surrounding whitespace.
func Clean(s string) string {
	for {
		start := strings.Index(s, thinkOpen)
		if start < 0 {
			break
		}
		end := strings.Index(s[start:], thinkClose)
		if end < 0 {
			// Unclosed block: drop it.
			s = s[:start]
			break
		}
		s = s[:start] + s[start+end+len(thinkClose):]
	}
	for {
		start := strings.Index(s, channelOpen)
		if start < 0 {
			break
		}
		end := strings.Index(s[start:], channelClose)
		if end < 0 {
			// No close marker: the answer may follow, so drop only the marker line.
			rest := s[start+len(channelOpen):]
			if nl := strings.IndexByte(rest, '\n'); nl >= 0 {
				rest = rest[nl+1:]
			} else {
				rest = ""
			}
			s = s[:start] + rest
			continue
		}
		s = s[:start] + s[start+end+len(channelClose):]
	}
	s = strings.ReplaceAll(s, channelClose, "") // a stray close marker
	return strings.TrimSpace(s)
}

// ExtractJSON extracts a JSON object from model output. It strips ``` /
// ```json fences, then returns the outermost {...} that parses as JSON.
func ExtractJSON(s string) ([]byte, error) {
	s = strings.TrimSpace(s)
	if i := strings.Index(s, "```"); i >= 0 {
		rest := s[i+len("```"):]
		if end := strings.Index(rest, "```"); end >= 0 {
			s = rest[:end]
		} else {
			s = rest
		}
		s = strings.TrimSpace(strings.TrimPrefix(s, "json"))
	}
	i := strings.Index(s, "{")
	if i < 0 {
		return nil, fmt.Errorf("llm: no JSON object in output: %s", first300([]byte(s)))
	}
	s = s[i:]
	end := balancedBrace(s)
	if end < 0 {
		return nil, fmt.Errorf("llm: unbalanced JSON in output: %s", first300([]byte(s)))
	}
	cand := s[:end+1]
	if !json.Valid([]byte(cand)) {
		return nil, fmt.Errorf("llm: invalid JSON in output: %s", first300([]byte(cand)))
	}
	return []byte(cand), nil
}

// balancedBrace returns the index of the '}' that closes the '{' at s[0],
// skipping braces inside JSON strings, or -1 if unbalanced.
func balancedBrace(s string) int {
	depth := 0
	inStr := false
	escaped := false
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch c {
		case '\\':
			// A backslash escapes the next byte only inside a string;
			// "\\" is an escaped backslash, so it does not set escaped.
			escaped = inStr && !escaped
		case '"':
			if !escaped {
				inStr = !inStr
			}
		case '{':
			if !inStr {
				depth++
			}
		case '}':
			if !inStr {
				depth--
				if depth == 0 {
					return i
				}
			}
		}
		if c != '\\' {
			escaped = false
		}
	}
	return -1
}
