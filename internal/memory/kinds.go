// Package memory extracts, merges and renders what the bot remembers: the
// items of members, of the group and of the bot itself (store.Memory).
package memory

import (
	"time"

	"github.com/ehsanranjbar/hooshang/internal/i18n"
	"github.com/ehsanranjbar/hooshang/internal/store"
	"github.com/ehsanranjbar/hooshang/internal/text"
)

// Policy says how a kind of item lives.
type Policy struct {
	Extract bool          // the extractor may add items of this kind
	TTL     time.Duration // 0 = durable
}

// Policies by kind. Orders and terms are only written by people (the admin,
// whoever teaches a term); events and samples by the group job; looks by the
// Telegram profile seed.
var Policies = map[string]Policy{
	store.KindFact:         {Extract: true},
	store.KindInterest:     {Extract: true},
	store.KindQuirk:        {Extract: true},
	store.KindRelationship: {Extract: true},
	store.KindBoundary:     {Extract: true},
	store.KindEpisode:      {Extract: true, TTL: 14 * 24 * time.Hour},
	store.KindStance:       {Extract: true},
	store.KindJoke:         {Extract: true},
	store.KindPromise:      {Extract: true, TTL: 7 * 24 * time.Hour},
	store.KindOrder:        {},
	store.KindLook:         {TTL: 90 * 24 * time.Hour},
	store.KindEvent:        {},
	store.KindTerm:         {},
	store.KindSample:       {},
}

// MemberKinds are the kinds the member extractor may return, in display order.
var MemberKinds = []string{store.KindFact, store.KindInterest, store.KindQuirk, store.KindRelationship,
	store.KindBoundary, store.KindEpisode, store.KindStance, store.KindJoke}

// SelfKinds are the kinds the self extractor may return.
var SelfKinds = []string{store.KindStance, store.KindJoke, store.KindPromise}

// RenderKinds are the member kinds shown in a profile (orders are shown on
// their own, as rules).
var RenderKinds = []string{store.KindFact, store.KindInterest, store.KindQuirk, store.KindRelationship,
	store.KindBoundary, store.KindEpisode, store.KindStance, store.KindJoke, store.KindLook}

// Sections are the names edit_memory accepts: the member kinds plus nickname
// and summary and the admin's orders.
func Sections() []string {
	return []string{"nickname", "summary", store.KindFact, store.KindInterest, store.KindQuirk,
		store.KindRelationship, store.KindBoundary, store.KindOrder, store.KindEpisode, store.KindStance, store.KindJoke}
}

// Title is the Persian title of a kind or section.
func Title(kind string) string {
	if t, ok := i18n.KindTitles[kind]; ok {
		return t
	}
	return kind
}

// Normalize folds Persian text for comparison (text.Squash: folded, no spaces).
func Normalize(s string) string { return text.Squash(s) }
