package bot

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mymmrac/telego"

	"github.com/ehsanranjbar/hooshang/internal/config"
	"github.com/ehsanranjbar/hooshang/internal/llm"
)

// fakeTranscriber answers with the text scripted for the audio bytes.
type fakeTranscriber struct {
	mu    sync.Mutex
	say   map[string]string // audio → transcript
	err   error
	calls int
}

func (f *fakeTranscriber) Transcribe(_ context.Context, audio []byte, _ string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	return f.say[string(audio)], f.err
}

func (f *fakeTranscriber) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

// voiceBot is a reply bot with voice transcription on, a background
// transcriber running, and the files "v1", "v2"... served as their own name.
func voiceBot(t *testing.T, say map[string]string, responses ...llm.Response) (*Bot, *fakeTG, *llm.Fake, *fakeTranscriber) {
	t.Helper()
	b, _, tg, fake := replyBot(t, 100, responses...)
	b.deps.Cfg.Bot.SpontaneousChance = 0
	b.deps.Cfg.Features.Voice = &config.VoiceFeature{Enabled: true, Language: "fa", MaxDuration: 120 * time.Second,
		MaxQueue: 10, MaxWords: 120, VideoNotes: true}
	ft := &fakeTranscriber{say: say}
	b.deps.Transcriber = ft
	files := map[string][]byte{}
	for audio := range say {
		files[audio] = []byte(audio)
	}
	tg.serveFiles(t, files)
	b.transcriber = newTranscriber(b)
	ctx, cancel := context.WithCancel(context.Background())
	go b.transcriber.run(ctx)
	t.Cleanup(func() {
		cancel()
		<-b.transcriber.done
		b.workers.stop(time.Second)
	})
	return b, tg, fake, ft
}

func voiceMsg(id int, from *telego.User, fileID string) *telego.Message {
	return mediaMsg(id, from, func(m *telego.Message) {
		m.Voice = &telego.Voice{FileID: fileID, FileUniqueID: "u-" + fileID, Duration: 5, MimeType: "audio/ogg"}
	})
}

// eventually waits up to 5 s for cond.
func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// settle waits until the background transcriber has finished everything queued
// so far: a job queued behind them runs only after them.
func settle(t *testing.T, b *Bot) {
	t.Helper()
	done := make(chan struct{})
	if !b.transcriber.submit("barrier", func(context.Context) { close(done) }) {
		t.Fatal("transcriber queue is full")
	}
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("transcriber did not settle")
	}
}

func storedText(b *Bot, id int64) string {
	m, err := b.deps.DB.GetMessage(testChat, id)
	if err != nil {
		return ""
	}
	return m.Text
}

func TestVoiceTranscriptIsStored(t *testing.T) {
	b, tg, fake, ft := voiceBot(t, map[string]string{"v1": " سلام بچه‌ها،\n امشب [موسیقی] کجا بریم؟ "})
	b.handleMessage(context.Background(), voiceMsg(1, sara, "v1"))
	eventually(t, "the transcript", func() bool { return storedText(b, 1) != "[ویس]" })
	if got := storedText(b, 1); got != "[ویس: سلام بچه‌ها، امشب کجا بریم؟]" {
		t.Fatalf("stored = %q", got)
	}
	settle(t, b)
	if m, _ := b.deps.DB.GetMessage(testChat, 1); !m.Described {
		t.Error("the message is not marked done")
	}
	if len(fake.Calls) != 0 || tg.sentCount() != 0 || ft.callCount() != 1 {
		t.Errorf("a voice for nobody got an answer: %d LLM calls, %d sent, %d transcriptions", len(fake.Calls), tg.sentCount(), ft.callCount())
	}
}

func TestVoiceThatSaysTheNameIsAnswered(t *testing.T) {
	b, tg, fake, _ := voiceBot(t, map[string]string{"v1": "هوشنگ چطوری", "v2": "سلام بچه‌ها"},
		llm.Response{Content: "خوبم، تو چطوری؟"})
	b.handleMessage(context.Background(), voiceMsg(2, ali, "v2")) // not for the bot
	b.handleMessage(context.Background(), voiceMsg(1, sara, "v1"))
	eventually(t, "the reply", func() bool { return tg.sentCount() == 1 })
	settle(t, b)
	if got := tg.sentTexts(); len(got) != 1 || got[0] != "خوبم، تو چطوری؟" {
		t.Fatalf("sent = %q", got)
	}
	if tg.sent[0].ReplyParameters == nil || tg.sent[0].ReplyParameters.MessageID != 1 {
		t.Errorf("the reply is not to the voice that called the bot: %+v", tg.sent[0].ReplyParameters)
	}
	if len(fake.Calls) != 1 {
		t.Fatalf("LLM calls = %d: the other voice must stay unanswered", len(fake.Calls))
	}
	prompt := fake.Calls[0].Messages[1].Content
	for _, want := range []string{">> [1] سارا (@sara): [ویس: هوشنگ چطوری]", "[2] علی (@ali): [ویس: سلام بچه‌ها]"} {
		if !strings.Contains(prompt, want) {
			t.Errorf("the prompt lacks %q:\n%s", want, prompt)
		}
	}
}

func TestVoiceReplyToTheBotIsTranscribedBeforeTheReply(t *testing.T) {
	b, tg, fake, ft := voiceBot(t, map[string]string{"v1": "آره دقیقاً همینه"}, llm.Response{Content: "پس موافقی"})
	say(b, 5, &telego.User{ID: testBotID, IsBot: true, FirstName: "هوشنگ"}, "فوتبال دوست داری؟")
	v := voiceMsg(6, ali, "v1")
	v.ReplyToMessage = &telego.Message{MessageID: 5, From: &telego.User{ID: testBotID, IsBot: true}}
	b.handleMessage(context.Background(), v)
	eventually(t, "the reply", func() bool { return tg.sentCount() == 1 })
	if len(fake.Calls) != 1 || !strings.Contains(fake.Calls[0].Messages[1].Content, ">> [6] علی (@ali) ↩︎[5]: [ویس: آره دقیقاً همینه]") {
		t.Fatalf("the transcript is not in the prompt: %+v", fake.Calls)
	}
	settle(t, b)
	if ft.callCount() != 1 {
		t.Errorf("transcribed %d times, want once", ft.callCount())
	}
}

func TestVoiceTriggerWaitsForTheBackgroundWorker(t *testing.T) {
	b, _, _, ft := voiceBot(t, map[string]string{"v1": "سلام"})
	m := voiceMsg(1, ali, "v1")
	b.storeIncoming(m, "[ویس]")
	if _, ok := b.transcriber.claim("v1"); !ok {
		t.Fatal("claim failed")
	}
	done := make(chan struct{})
	go func() { b.transcribeTrigger(context.Background(), m); close(done) }()
	select {
	case <-done:
		t.Fatal("transcribeTrigger did not wait for the in-flight transcription")
	case <-time.After(50 * time.Millisecond):
	}
	b.applyDescription(describeJob{chatID: testChat, msgID: 1, tag: mediaTag(m)}, "سلام")
	b.transcriber.release("v1")
	<-done
	if storedText(b, 1) != "[ویس: سلام]" || ft.callCount() != 0 {
		t.Fatalf("stored %q, transcriptions %d", storedText(b, 1), ft.callCount())
	}
}

func TestLongVoiceIsLeftAlone(t *testing.T) {
	b, tg, fake, ft := voiceBot(t, map[string]string{"v1": "هوشنگ سلام"}, llm.Response{Content: "جانم"})
	long := voiceMsg(1, sara, "v1")
	long.Voice.Duration = 121
	b.handleMessage(context.Background(), long)
	settle(t, b)
	if ft.callCount() != 0 || storedText(b, 1) != "[ویس]" {
		t.Fatalf("a voice over max_duration was transcribed: %q, %d calls", storedText(b, 1), ft.callCount())
	}
	// A long voice that replies to the bot is still answered, as «[ویس]».
	long2 := voiceMsg(2, sara, "v1")
	long2.Voice.Duration = 600
	long2.ReplyToMessage = &telego.Message{MessageID: 9, From: &telego.User{ID: testBotID, IsBot: true}}
	b.handleMessage(context.Background(), long2)
	eventually(t, "the reply", func() bool { return tg.sentCount() == 1 })
	if ft.callCount() != 0 || len(fake.Calls) != 1 || !strings.Contains(fake.Calls[0].Messages[1].Content, "[ویس]") {
		t.Errorf("calls %d, LLM calls %d", ft.callCount(), len(fake.Calls))
	}
	if _, ok := b.voiceRefOf(voiceMsg(3, sara, "v1")); !ok {
		t.Error("a 5 s voice is not transcribable")
	}
}

func TestBacklogVoiceIsTranscribedNotAnswered(t *testing.T) {
	b, tg, fake, _ := voiceBot(t, map[string]string{"v1": "هوشنگ چطوری"}, llm.Response{Content: "x"})
	old := voiceMsg(1, sara, "v1")
	old.Date = time.Now().Add(-10 * time.Minute).Unix() // older than staleWindow, younger than the test store's retention
	b.handleMessage(context.Background(), old)
	eventually(t, "the transcript", func() bool { return storedText(b, 1) == "[ویس: هوشنگ چطوری]" })
	settle(t, b)
	b.workers.stop(time.Second)
	if len(fake.Calls) != 0 || tg.sentCount() != 0 {
		t.Fatalf("the backlog was answered: %d LLM calls, %d sent", len(fake.Calls), tg.sentCount())
	}
}

func TestVoiceFeatureOffChangesNothing(t *testing.T) {
	b, _, tg, fake := replyBot(t, 100, llm.Response{Content: "نظر من اینه"})
	ft := &fakeTranscriber{say: map[string]string{"v1": "هوشنگ"}}
	b.deps.Transcriber = ft // present, but the feature is not in the config
	tg.serveFiles(t, map[string][]byte{"v1": []byte("v1")})
	b.deps.Cfg.Bot.SpontaneousChance = 1
	b.handleMessage(context.Background(), voiceMsg(1, sara, "v1"))
	// As before: stored as [ویس], and the spontaneous roll happens at once.
	eventually(t, "the spontaneous message", func() bool { return tg.sentCount() == 1 })
	if ft.callCount() != 0 || storedText(b, 1) != "[ویس]" || len(fake.Calls) != 1 {
		t.Errorf("transcriptions %d, stored %q, LLM calls %d", ft.callCount(), storedText(b, 1), len(fake.Calls))
	}
	if _, ok := b.voiceRefOf(voiceMsg(2, sara, "v1")); ok {
		t.Error("voiceRefOf found a voice with the feature off")
	}
	b.workers.stop(time.Second)
}

func TestVideoNotesFollowTheirSetting(t *testing.T) {
	b, _, _, _ := voiceBot(t, map[string]string{"n1": "سلام از دوربین"})
	note := mediaMsg(1, sara, func(m *telego.Message) {
		m.VideoNote = &telego.VideoNote{FileID: "n1", FileUniqueID: "u-n1", Duration: 4}
	})
	b.handleMessage(context.Background(), note)
	eventually(t, "the transcript", func() bool { return storedText(b, 1) != "[ویدیو مسیج]" })
	if got := storedText(b, 1); got != "[ویدیو مسیج: سلام از دوربین]" {
		t.Fatalf("stored = %q", got)
	}
	b.deps.Cfg.Features.Voice.VideoNotes = false
	if _, ok := b.voiceRefOf(note); ok {
		t.Error("video notes are off but got a ref")
	}
}

func TestFailedOrEmptyTranscriptionKeepsTheTag(t *testing.T) {
	b, _, _, ft := voiceBot(t, map[string]string{"v1": "", "v2": "x"})
	b.handleMessage(context.Background(), voiceMsg(1, sara, "v1")) // silence
	settle(t, b)
	if m, _ := b.deps.DB.GetMessage(testChat, 1); m.Text != "[ویس]" || !m.Described {
		t.Errorf("silence: %+v", m)
	}
	ft.mu.Lock()
	ft.err = errors.New("whisper crashed")
	ft.mu.Unlock()
	b.handleMessage(context.Background(), voiceMsg(2, sara, "v2"))
	settle(t, b)
	if m, _ := b.deps.DB.GetMessage(testChat, 2); m.Text != "[ویس]" || m.Described {
		t.Errorf("failure: %+v (it may be tried again)", m)
	}
}

func TestCleanTranscript(t *testing.T) {
	if got := cleanTranscript("یک [دو] سه\nچهار", 120); got != "یک سه چهار" {
		t.Errorf("clean = %q", got)
	}
	if got := cleanTranscript("یک (دو] سه چهار پنج", 3); got != "یک (دو) سه…" {
		t.Errorf("cap = %q", got)
	}
}
