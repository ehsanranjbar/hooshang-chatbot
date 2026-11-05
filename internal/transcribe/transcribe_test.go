package transcribe

import (
	"context"
	"encoding/binary"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// modelPath is the model the integration tests use (WHISPER_MODEL, else the
// one the README downloads into models/).
func modelPath() string {
	if p := os.Getenv("WHISPER_MODEL"); p != "" {
		return p
	}
	return "../../models/ggml-large-v3-turbo-q5_0.bin"
}

// sine makes a 2 s Ogg/Opus clip with ffmpeg, or skips the test.
func sine(t *testing.T) []byte {
	t.Helper()
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg not installed")
	}
	out := filepath.Join(t.TempDir(), "sine.ogg")
	cmd := exec.Command(ffmpeg, "-nostdin", "-loglevel", "error", "-f", "lavfi", "-i", "sine=frequency=440:duration=2",
		"-c:a", "libopus", out)
	if msg, err := cmd.CombinedOutput(); err != nil {
		t.Skipf("ffmpeg cannot write Opus: %v: %s", err, msg)
	}
	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestToWAVIs16kMono(t *testing.T) {
	clip := sine(t)
	w := NewWhisper(Options{})
	if w.ffmpeg == "" {
		t.Skip("ffmpeg not installed")
	}
	dir := t.TempDir()
	path, err := w.toWAV(context.Background(), dir, clip, "audio/ogg")
	if err != nil {
		t.Fatal(err)
	}
	wav, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(wav) < 44 || string(wav[:4]) != "RIFF" || string(wav[8:12]) != "WAVE" {
		t.Fatalf("not a WAV file: % x", wav[:min(12, len(wav))])
	}
	channels, rate, bits := binary.LittleEndian.Uint16(wav[22:]), binary.LittleEndian.Uint32(wav[24:]), binary.LittleEndian.Uint16(wav[34:])
	if channels != 1 || rate != 16000 || bits != 16 {
		t.Errorf("channels %d, rate %d, bits %d; want 1, 16000, 16", channels, rate, bits)
	}
	if secs := float64(len(wav)-44) / (16000 * 2); secs < 1.8 || secs > 2.3 {
		t.Errorf("duration %.2fs, want about 2s", secs)
	}
}

func TestTranscribeRealWhisper(t *testing.T) {
	clip := sine(t)
	model := modelPath()
	if _, err := os.Stat(model); err != nil {
		t.Skipf("model %s not found", model)
	}
	w := NewWhisper(Options{Model: model, Language: "fa"})
	if w.whisper == "" {
		t.Skip("whisper-cli not installed")
	}
	if err := w.Check(); err != nil {
		t.Fatalf("Check: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	text, err := w.Transcribe(ctx, clip, "audio/ogg")
	if err != nil {
		t.Fatal(err)
	}
	// A tone has no speech: the text may be empty or gibberish, but it is one
	// line without timestamps.
	if strings.ContainsAny(text, "\r\n") || strings.Contains(text, "-->") {
		t.Errorf("not one plain line: %q", text)
	}
	t.Logf("transcript of a sine tone: %q", text)
	leftovers, _ := filepath.Glob(filepath.Join(os.TempDir(), "hooshang-voice-*"))
	if len(leftovers) > 0 {
		t.Errorf("temp files left behind: %v", leftovers)
	}
}

func TestCheckReportsWhatIsMissing(t *testing.T) {
	w := NewWhisper(Options{Whisper: "/nonexistent/whisper-cli", FFmpeg: "/nonexistent/ffmpeg", Model: "/nonexistent/model.bin"})
	err := w.Check()
	if err == nil {
		t.Fatal("Check passed with nothing installed")
	}
	for _, want := range []string{"whisper-cli not found", "ffmpeg not found", "model"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("Check error lacks %q: %v", want, err)
		}
	}
	if _, err := w.Transcribe(context.Background(), []byte("x"), "audio/ogg"); err == nil {
		t.Error("Transcribe ran without whisper")
	}
}

func TestCleanMakesOneLine(t *testing.T) {
	for _, tc := range []struct {
		in    string
		words int
		want  string
	}{
		{" سلام  \n خوبی؟\n", 0, "سلام خوبی؟"},
		{"[BLANK_AUDIO]\n", 0, ""},
		{"سلام [موسیقی] دوست\tمن\r\n", 0, "سلام دوست من"},
		{"یک دو سه چهار پنج", 3, "یک دو سه…"},
		{"یک دو", 3, "یک دو"},
	} {
		if got := Clean(tc.in, tc.words); got != tc.want {
			t.Errorf("Clean(%q, %d) = %q, want %q", tc.in, tc.words, got, tc.want)
		}
	}
}

func TestExtension(t *testing.T) {
	for mime, want := range map[string]string{"audio/ogg": ".ogg", "Video/MP4": ".mp4", "audio/mpeg": ".mp3", "": ".bin", "x/y": ".bin"} {
		if got := extension(mime); got != want {
			t.Errorf("extension(%q) = %q, want %q", mime, got, want)
		}
	}
}
