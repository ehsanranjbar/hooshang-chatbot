package bot

import (
	"context"
	"sync"
	"time"

	"github.com/mymmrac/telego"
)

// jobKind selects how a job is executed.
type jobKind int

const (
	jobReply jobKind = iota
	jobSpontaneous
	jobFunc     // runs fn: commands that do network or LLM work (faal, poem)
	jobReaction // a spontaneous emoji reaction to msg
)

// job is one unit of work for a chat worker.
type job struct {
	kind       jobKind
	chatID     int64
	msg        *telego.Message // the triggering message (nil for spontaneous)
	mode       string          // spontaneous mode (or "roast" for /roast)
	target     int64           // spontaneous/roast target user id (0 if none)
	targetName string          // rendered target name
	fn         func(ctx context.Context)
	answered   *bool     // set by a tool that fully answered the request (the faal)
	asOf       time.Time // DumpPrompt only: render the chat as it was then, up to msg
}

// worker serves a single chat. Its fields are guarded by workers.mu.
type worker struct {
	chatID int64
	queue  chan job
	busy   bool
}

const (
	workerIdle     = 10 * time.Minute // a worker exits after this long without a job
	typingInterval = 4 * time.Second  // how often "typing…" is sent while a job runs
	workerQueueCap = 5                // per-chat queue capacity
)

// workers owns the per-chat workers. Creating a worker, enqueueing into it and
// removing it when idle all happen under mu, so a job can never be put on the
// queue of a worker that is exiting.
type workers struct {
	mu     sync.Mutex
	byChat map[int64]*worker
	bot    *Bot
	ctx    context.Context // cancelled by stop; every job runs with it
	cancel context.CancelFunc
	wg     sync.WaitGroup
}

func newWorkers(b *Bot) *workers {
	ctx, cancel := context.WithCancel(context.Background())
	return &workers{byChat: make(map[int64]*worker), bot: b, ctx: ctx, cancel: cancel}
}

// Len reports how many workers exist (for /status).
func (w *workers) Len() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return len(w.byChat)
}

// enqueue adds a job to its chat's worker, creating the worker on demand.
// A spontaneous job is only accepted when the worker is idle with an empty
// queue. It reports whether the job was accepted.
func (w *workers) enqueue(j job, spontaneous bool) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.ctx.Err() != nil {
		return false
	}
	wk, ok := w.byChat[j.chatID]
	if !ok {
		wk = &worker{chatID: j.chatID, queue: make(chan job, workerQueueCap)}
		w.byChat[j.chatID] = wk
		w.wg.Add(1)
		go w.run(wk)
	}
	if spontaneous && (wk.busy || len(wk.queue) > 0) {
		return false
	}
	select {
	case wk.queue <- j:
		return true
	default:
		return false
	}
}

// run is the worker loop.
func (w *workers) run(wk *worker) {
	defer w.wg.Done()
	idle := time.NewTimer(workerIdle)
	defer idle.Stop()
	for {
		select {
		case <-w.ctx.Done():
			return
		case j := <-wk.queue:
			w.setBusy(wk, true)
			w.bot.runJob(w.ctx, j)
			w.setBusy(wk, false)
			idle.Reset(workerIdle)
		case <-idle.C:
			w.mu.Lock()
			if len(wk.queue) == 0 && !wk.busy {
				delete(w.byChat, wk.chatID)
				w.mu.Unlock()
				return
			}
			w.mu.Unlock()
			idle.Reset(workerIdle)
		}
	}
}

func (w *workers) setBusy(wk *worker, busy bool) {
	w.mu.Lock()
	wk.busy = busy
	w.mu.Unlock()
}

// stop cancels the running jobs and waits up to timeout for the workers to exit.
func (w *workers) stop(timeout time.Duration) {
	w.cancel()
	done := make(chan struct{})
	go func() { w.wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(timeout):
		w.bot.logger.Warn("workers did not stop in time")
	}
}

// enqueueReply adds a reply job; it is dropped (with a log) when the queue is full.
func (b *Bot) enqueueReply(j job) {
	if !b.workers.enqueue(j, false) {
		b.logger.Info("reply dropped: chat queue full", "chat", j.chatID)
	}
}

// enqueueSpontaneous adds a spontaneous job; it is dropped if the worker is busy
// or has queued work.
func (b *Bot) enqueueSpontaneous(j job) {
	if !b.workers.enqueue(j, true) {
		b.logger.Debug("spontaneous dropped: worker busy", "chat", j.chatID)
	}
}

// enqueueFunc runs fn on the chat's worker, so slow commands don't block the
// update loop.
func (b *Bot) enqueueFunc(chatID int64, fn func(ctx context.Context)) {
	if !b.workers.enqueue(job{kind: jobFunc, chatID: chatID, fn: fn}, false) {
		b.logger.Info("command dropped: chat queue full", "chat", chatID)
	}
}

// runJob runs one job, sending the typing action right away and then every 4s
// (except for reactions).
func (b *Bot) runJob(ctx context.Context, j job) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	if j.kind == jobReaction { // a reaction is silent: no "typing…"
		b.runReaction(ctx, j)
		return
	}
	go func() {
		t := time.NewTicker(typingInterval)
		defer t.Stop()
		for {
			_ = b.deps.TG.SendChatAction(ctx, &telego.SendChatActionParams{
				ChatID: newChatID(j.chatID),
				Action: telego.ChatActionTyping,
			})
			select {
			case <-ctx.Done():
				return
			case <-t.C:
			}
		}
	}()

	switch j.kind {
	case jobReply:
		b.runReply(ctx, j)
	case jobSpontaneous:
		b.runSpontaneous(ctx, j)
	case jobFunc:
		j.fn(ctx)
	}
}
