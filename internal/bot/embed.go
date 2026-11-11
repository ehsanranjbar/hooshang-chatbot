package bot

import (
	"context"
	"fmt"
	"hash/fnv"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/mymmrac/telego"

	"github.com/ehsanranjbar/hooshang/internal/i18n"
	"github.com/ehsanranjbar/hooshang/internal/llm"
	"github.com/ehsanranjbar/hooshang/internal/prompts"
	"github.com/ehsanranjbar/hooshang/internal/rag"
	"github.com/ehsanranjbar/hooshang/internal/store"
)

// embedRef names one text to embed.
type embedRef struct {
	kind   string // "msg", "item", "media"
	chatID int64
	id     int64  // message id, or entity for items
	itemID uint64 // items
	media  string // media: the file_unique_id
	text   string
	hash   uint64
}

// embedder embeds texts in batches in the background into the vector stores
// of the database, which retrieval searches. It is nil when features.rag is
// off or no embedding model is configured.
type embedder struct {
	b        *Bot
	client   llm.Embedder
	model    string
	mu       sync.Mutex
	pending  []embedRef
	wake     chan struct{}
	done     chan struct{}
	failed   int
	embedded int
}

// newEmbedder returns the embedder, or nil when retrieval is off.
func newEmbedder(b *Bot) *embedder {
	r := b.deps.Cfg.Features.RAG
	model := b.deps.Cfg.LLM.EmbeddingModel
	if !r.On() || model == "" {
		return nil
	}
	e, ok := b.deps.LLM.(llm.Embedder)
	if !ok {
		return nil
	}
	return &embedder{b: b, client: e, model: model, wake: make(chan struct{}, 1), done: make(chan struct{})}
}

// textHash is the FNV-64a of a text.
func textHash(s string) uint64 {
	h := fnv.New64a()
	h.Write([]byte(s))
	return h.Sum64()
}

// submit queues a text; when the queue is full it is dropped (the backfill
// picks archived messages up later).
func (e *embedder) submit(r embedRef) {
	r.text = strings.TrimSpace(r.text)
	if r.text == "" {
		return
	}
	r.hash = textHash(r.text)
	e.mu.Lock()
	if len(e.pending) >= e.b.deps.Cfg.Features.RAG.MaxQueue {
		e.mu.Unlock()
		return
	}
	e.pending = append(e.pending, r)
	e.mu.Unlock()
	select {
	case e.wake <- struct{}{}:
	default:
	}
}

// run serves the queue until ctx is done, one batch at a time.
func (e *embedder) run(ctx context.Context) {
	defer close(e.done)
	e.backfill()
	for {
		select {
		case <-ctx.Done():
			return
		case <-e.wake:
		}
		for e.flush(ctx) {
			if ctx.Err() != nil {
				return
			}
		}
	}
}

// backfill queues archived messages that have no vector yet.
func (e *embedder) backfill() {
	chats, err := e.b.deps.DB.ChatIDs()
	if err != nil {
		return
	}
	for _, chatID := range chats {
		ids, err := e.b.deps.DB.UnembeddedArchive(chatID, e.b.deps.Cfg.Features.RAG.MaxQueue/2)
		if err != nil {
			continue
		}
		for _, id := range ids {
			if a, err := e.b.deps.DB.GetArchived(chatID, id); err == nil {
				e.submit(embedRef{kind: "msg", chatID: chatID, id: id, text: a.Text})
			}
		}
	}
}

// flush embeds one batch; it reports whether anything was pending.
func (e *embedder) flush(ctx context.Context) bool {
	e.mu.Lock()
	n := min(len(e.pending), e.b.deps.Cfg.Features.RAG.Batch)
	batch := append([]embedRef(nil), e.pending[:n]...)
	e.pending = e.pending[n:]
	e.mu.Unlock()
	if len(batch) == 0 {
		return false
	}
	texts := make([]string, len(batch))
	for i, r := range batch {
		texts[i] = r.text
	}
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	vecs, err := e.client.Embed(ctx, e.model, texts)
	if err != nil {
		e.mu.Lock()
		e.failed += len(batch)
		e.mu.Unlock()
		e.b.logger.Warn("embedding failed", "n", len(batch), "err", err)
		return true
	}
	ttl := e.b.deps.Cfg.Features.RAG.ArchiveTTL
	for i, r := range batch {
		v := store.Embedding{Vec: vecs[i], Hash: r.hash, At: time.Now()}
		var err error
		switch r.kind {
		case "msg":
			err = e.b.deps.DB.PutMsgVector(r.chatID, r.id, v, ttl)
		case "item":
			err = e.b.deps.DB.PutItemVector(r.chatID, r.id, r.itemID, v)
		case "media":
			err = e.b.deps.DB.PutMediaVector(r.media, v, e.mediaTTL())
		}
		if err != nil {
			// A vector of the wrong dimensions (embedding_dims does not match
			// the model) or of zeros is refused by the index.
			e.b.logger.Warn("storing a vector failed", "kind", r.kind, "err", err)
		}
	}
	e.mu.Lock()
	e.embedded += len(batch)
	e.mu.Unlock()
	return true
}

// mediaTTL is the media cache TTL (vectors live as long as the descriptions).
func (e *embedder) mediaTTL() time.Duration {
	if m := e.b.deps.Cfg.Features.Media; m != nil {
		return m.CacheTTL
	}
	return 90 * 24 * time.Hour
}

// query embeds one text now (for a reply), with a short timeout; ok is false
// on any failure so callers fall back to the non-retrieval path.
func (e *embedder) query(ctx context.Context, text string) ([]float32, bool) {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil, false
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	vecs, err := e.client.Embed(ctx, e.model, []string{text})
	if err != nil || len(vecs) != 1 {
		e.b.logger.Debug("query embedding failed", "err", err)
		return nil, false
	}
	return vecs[0], true
}

// status is one line for /status.
func (e *embedder) status() string {
	st, _ := e.b.deps.DB.MsgIndexStats()
	e.mu.Lock()
	defer e.mu.Unlock()
	return fmt.Sprintf("%s, %d partitions, %d unplaced, %d embedded, %d failed, %d pending",
		e.model, st.Partitions, st.Unplaced, e.embedded, e.failed, len(e.pending))
}

// ---------------------------------------------------------------- hooks

// archivable reports whether a member message is worth archiving: text of at
// least 12 runes that is not a command or an undescribed media tag.
func archivable(text string) bool {
	t := strings.TrimSpace(text)
	return len([]rune(t)) >= 12 && !strings.HasPrefix(t, "/") && !(strings.HasPrefix(t, "[") && strings.HasSuffix(t, "]"))
}

// archive keeps a member message for retrieval and queues its embedding.
func (b *Bot) archive(chatID int64, m *telego.Message, text string) {
	if b.embedder == nil || m.From == nil || !archivable(text) {
		return
	}
	a := store.Archived{UserID: m.From.ID, Name: displayNameOf(m), Text: text, Date: time.Unix(m.Date, 0)}
	if err := b.deps.DB.Archive(chatID, int64(m.MessageID), a, b.deps.Cfg.Features.RAG.ArchiveTTL); err != nil {
		return
	}
	b.embedder.submit(embedRef{kind: "msg", chatID: chatID, id: int64(m.MessageID), text: a.Name + ": " + text})
}

// rearchive updates an archived message's text (a media description came in).
func (b *Bot) rearchive(chatID, msgID int64, text string) {
	if b.embedder == nil {
		return
	}
	a, err := b.deps.DB.GetArchived(chatID, msgID)
	if err != nil {
		if m, err := b.deps.DB.GetMessage(chatID, msgID); err == nil && !m.FromBot {
			a = &store.Archived{UserID: m.UserID, Name: m.Name, Date: m.Date}
		} else {
			return
		}
	}
	a.Text = text
	if err := b.deps.DB.Archive(chatID, msgID, *a, b.deps.Cfg.Features.RAG.ArchiveTTL); err == nil {
		b.embedder.submit(embedRef{kind: "msg", chatID: chatID, id: msgID, text: a.Name + ": " + text})
	}
}

// purgeArchive forgets a member's archived messages and vectors (/forgetme, /ignoreme).
func (b *Bot) purgeArchive(chatID, userID int64) {
	_ = b.deps.DB.DeleteEntityVectors(chatID, userID)
	if b.embedder == nil {
		return
	}
	_, _ = b.deps.DB.PurgeArchive(chatID, userID)
}

// embedItems queues the embedding of memory items whose text changed since
// they were last embedded.
func (b *Bot) embedItems(chatID, entity int64, items []store.Item) {
	if b.embedder == nil {
		return
	}
	have, _ := b.deps.DB.ItemVectors(chatID, entity)
	for _, it := range items {
		if it.Kind == store.KindSample {
			continue
		}
		text := it.Text
		if it.Kind == store.KindTerm {
			text = it.Text + ": " + it.Note
		}
		if v, ok := have[it.ID]; ok && v.Hash == textHash(text) {
			continue
		}
		b.embedder.submit(embedRef{kind: "item", chatID: chatID, id: entity, itemID: it.ID, text: text})
	}
}

// forgetItemVector drops one item's vector.
func (b *Bot) forgetItemVector(chatID, entity int64, itemID uint64) {
	_ = b.deps.DB.DeleteItemVector(chatID, entity, itemID)
}

// embedMedia queues the embedding of a cached sticker/GIF description.
func (b *Bot) embedMedia(uniqueID, text string) {
	if b.embedder == nil {
		return
	}
	b.embedder.submit(embedRef{kind: "media", media: uniqueID, text: text})
}

// ---------------------------------------------------------------- retrieval

// queryVector embeds the retrieval query of a job: the trigger text plus the
// message it replies to; for a job without a trigger, the last three human
// lines of the transcript. It is computed once per job.
func (b *Bot) queryVector(ctx context.Context, j *job, msgs []store.Message) ([]float32, bool) {
	if b.embedder == nil {
		return nil, false
	}
	if j.queried {
		return j.queryVec, j.queryOK
	}
	var parts []string
	if j.msg != nil {
		if r := j.msg.ReplyToMessage; r != nil && r.Text != "" {
			parts = append(parts, r.Text)
		}
		t := j.msg.Text
		if t == "" {
			t = j.msg.Caption
		}
		parts = append(parts, t)
	} else {
		n := 0
		for i := len(msgs) - 1; i >= 0 && n < 3; i-- {
			if !msgs[i].FromBot {
				parts = append([]string{msgs[i].Text}, parts...)
				n++
			}
		}
	}
	v, ok := b.embedder.query(ctx, strings.Join(parts, "\n"))
	j.queryVec, j.queryOK, j.queried = v, ok, true
	return v, ok
}

// relatedMessages renders the archived messages most relevant to the job
// that are not already in the transcript, with their Persian dates.
func (b *Bot) relatedMessages(ctx context.Context, j *job, msgs []store.Message) string {
	r := b.deps.Cfg.Features.RAG
	if b.embedder == nil || r.TopK <= 0 || !worthRetrieving(j) {
		return ""
	}
	q, ok := b.queryVector(ctx, j, msgs)
	if !ok {
		return ""
	}
	inTranscript := map[int64]bool{}
	for _, m := range msgs {
		inTranscript[m.ID] = true
	}
	now := time.Now()
	half := time.Duration(r.DecayDays) * 24 * time.Hour
	// The nearest messages are re-ranked by their age, so more are asked for
	// than are kept, and as many more as the transcript may hide.
	found, err := b.deps.DB.SearchMessages(j.chatID, q, 4*r.TopK+len(msgs))
	if err != nil {
		b.logger.Warn("message search failed", "chat", j.chatID, "err", err)
		return ""
	}
	type hit struct {
		store.Hit
		a *store.Archived
	}
	var hits []hit
	for _, h := range found {
		if h.Score < float32(r.MinScore) || inTranscript[h.ID] {
			continue
		}
		a, err := b.deps.DB.GetArchived(j.chatID, h.ID)
		if err != nil {
			continue // expired before its vector
		}
		h.Score *= rag.Decay(now.Sub(a.Date), half)
		hits = append(hits, hit{h, a})
	}
	sort.SliceStable(hits, func(i, k int) bool { return hits[i].Score > hits[k].Score })
	if len(hits) > r.TopK {
		hits = hits[:r.TopK]
	}
	var lines []string
	var scores []string
	seen := map[string]bool{}
	for _, h := range hits {
		a := h.a
		if seen[a.Text] {
			continue
		}
		seen[a.Text] = true
		lines = append(lines, fmt.Sprintf("- (%s) %s: %s", i18n.When(a.Date, now), a.Name, a.Text))
		scores = append(scores, fmt.Sprintf("%d:%.2f", h.ID, h.Score))
	}
	if len(lines) > 0 {
		b.logger.Debug("related messages", "chat", j.chatID, "hits", strings.Join(scores, " "))
	}
	return strings.Join(lines, "\n")
}

// minQueryRunes is the shortest trigger worth a retrieval: shorter texts
// («باش», «شب بخیر») embed close to everything and only pull in noise.
const minQueryRunes = 12

// worthRetrieving reports whether a job's trigger says enough to look for
// older messages: spontaneous jobs always do (their query is the recent
// chatter); a reply does unless its trigger is a greeting or too short, in
// which case the replied-to message has to carry it.
func worthRetrieving(j *job) bool {
	if j.msg == nil {
		return true
	}
	t := j.msg.Text
	if t == "" {
		t = j.msg.Caption
	}
	if r := j.msg.ReplyToMessage; r != nil && utf8.RuneCountInString(strings.TrimSpace(r.Text)) >= minQueryRunes {
		return true
	}
	return utf8.RuneCountInString(strings.TrimSpace(t)) >= minQueryRunes && greetingKind(t) == ""
}

// selectItems picks the items of a member's memory worth their share of the
// budget: every locked item, order, boundary and recent episode, then the
// items closest to the job's query, until the rendered size fits. ok is false
// when retrieval is off or the query failed.
func (b *Bot) selectItems(j job, entity int64, mem store.Memory, share int) ([]store.Item, bool) {
	if b.embedder == nil || !j.queryOK {
		return nil, false
	}
	scores, err := b.deps.DB.ScoreItems(j.chatID, entity, j.queryVec)
	if err != nil {
		return nil, false
	}
	now := time.Now()
	var must, rest []store.Item
	for _, it := range mem.Items {
		switch {
		case it.Locked, it.Kind == store.KindOrder, it.Kind == store.KindBoundary,
			it.Kind == store.KindEpisode && now.Sub(it.LastAt) < 7*24*time.Hour:
			must = append(must, it)
		default:
			rest = append(rest, it)
		}
	}
	type scored struct {
		it store.Item
		s  float32
	}
	var ranked []scored
	for _, it := range rest {
		s, ok := scores[it.ID]
		if !ok {
			s = -1
		}
		ranked = append(ranked, scored{it, s})
	}
	sort.SliceStable(ranked, func(i, k int) bool { return ranked[i].s > ranked[k].s })
	out := must
	for _, r := range ranked {
		if len([]rune(prompts.Compact(out, r.it))) > share {
			break
		}
		out = append(out, r.it)
	}
	return out, true
}

// relatedTerms adds glossary terms close in meaning to the transcript when
// the literal matches left room (at most 2, cosine ≥ 0.6).
func (b *Bot) relatedTerms(chatID int64, transcript string, have []store.Item, inPrompt int) []store.Item {
	if b.embedder == nil || len(have) >= inPrompt {
		return nil
	}
	mem, err := b.deps.DB.GetMemory(chatID, store.GroupEntity)
	if err != nil {
		return nil
	}
	terms := mem.Kind(store.KindTerm)
	if len(terms) == 0 {
		return nil
	}
	q, ok := b.embedder.query(context.Background(), lastLines(transcript, 3))
	if !ok {
		return nil
	}
	scores, err := b.deps.DB.ScoreItems(chatID, store.GroupEntity, q)
	if err != nil {
		return nil
	}
	seen := map[uint64]bool{}
	for _, t := range have {
		seen[t.ID] = true
	}
	type scored struct {
		it store.Item
		s  float32
	}
	var ranked []scored
	for _, t := range terms {
		if s, ok := scores[t.ID]; ok && !seen[t.ID] && s >= 0.6 {
			ranked = append(ranked, scored{t, s})
		}
	}
	sort.Slice(ranked, func(i, k int) bool { return ranked[i].s > ranked[k].s })
	var out []store.Item
	for i := 0; i < len(ranked) && i < 2; i++ {
		out = append(out, ranked[i].it)
	}
	return out
}

// relatedMedia finds cached stickers/GIFs close in meaning to the query.
func (b *Bot) relatedMedia(ctx context.Context, kind, query string, n int, skip map[string]bool) []store.MediaDesc {
	if b.embedder == nil {
		return nil
	}
	q, ok := b.embedder.query(ctx, query)
	if !ok {
		return nil
	}
	hits, err := b.deps.DB.NearestMedia(q, n*2)
	if err != nil {
		b.logger.Warn("media search failed", "err", err)
		return nil
	}
	var out []store.MediaDesc
	for _, h := range hits {
		if h.Score < 0.5 {
			break
		}
		m, err := b.deps.DB.MediaByHash(h.ID)
		if err != nil || m.FileID == "" || skip[m.UniqueID] || (kind != "" && m.Kind != kind) {
			continue
		}
		out = append(out, *m)
		if len(out) == n {
			break
		}
	}
	return out
}

// lastLines returns the last n lines of a text.
func lastLines(s string, n int) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}
