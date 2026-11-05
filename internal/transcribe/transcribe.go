// Package transcribe turns speech into text. Whisper is the implementation:
// it converts the audio with ffmpeg and runs whisper.cpp's whisper-cli.
package transcribe

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Transcriber turns a recording into one line of text. mime is what the
// sender's client said the file is ("audio/ogg"); it only picks a file
// extension, since ffmpeg recognizes the content itself.
type Transcriber interface {
	Transcribe(ctx context.Context, audio []byte, mime string) (string, error)
}

// timeout bounds one transcription (conversion and recognition together).
const timeout = 5 * time.Minute

// Options configure Whisper.
type Options struct {
	Whisper  string // whisper-cli: a path or a name; empty = "whisper-cli" in PATH
	FFmpeg   string // ffmpeg: a path or a name; empty = "ffmpeg" in PATH
	Model    string // a multilingual ggml model file (never a .en one for Persian)
	Language string // spoken language, e.g. "fa"; "auto" detects it
	Threads  int    // 0 = whisper's default
}

// Whisper transcribes with whisper.cpp. One transcription runs at a time: the
// model uses the whole machine.
type Whisper struct {
	opts    Options
	whisper string // resolved path; "" = not found
	ffmpeg  string
	mu      sync.Mutex
}

// NewWhisper resolves the binaries. It never fails: Check reports what is
// missing, and Transcribe returns that error.
func NewWhisper(o Options) *Whisper {
	return &Whisper{opts: o, whisper: lookPath(o.Whisper, "whisper-cli"), ffmpeg: lookPath(o.FFmpeg, "ffmpeg")}
}

// lookPath resolves name (or def when empty) as a path or in PATH; "" if not found.
func lookPath(name, def string) string {
	if name == "" {
		name = def
	}
	p, err := exec.LookPath(name)
	if err != nil {
		return ""
	}
	return p
}

// Paths returns what was resolved: whisper-cli and ffmpeg ("" = not found)
// and the model path as configured.
func (w *Whisper) Paths() (whisper, ffmpeg, model string) {
	return w.whisper, w.ffmpeg, w.opts.Model
}

// Check reports everything that is missing (whisper-cli, ffmpeg, the model
// file) in one error, or nil when a transcription can run.
func (w *Whisper) Check() error {
	var errs []error
	if w.whisper == "" {
		errs = append(errs, fmt.Errorf("whisper-cli not found (brew install whisper-cpp, or set features.voice.whisper)"))
	}
	if w.ffmpeg == "" {
		errs = append(errs, fmt.Errorf("ffmpeg not found (brew install ffmpeg, or set features.media.ffmpeg)"))
	}
	if st, err := os.Stat(w.opts.Model); err != nil || st.IsDir() {
		errs = append(errs, fmt.Errorf("model %q not found", w.opts.Model))
	}
	return errors.Join(errs...)
}

// Transcribe converts the audio to 16 kHz mono WAV, runs whisper-cli on it and
// returns the text on one line, without annotations like [Music].
func (w *Whisper) Transcribe(ctx context.Context, audio []byte, mime string) (string, error) {
	if err := w.Check(); err != nil {
		return "", err
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	dir, err := os.MkdirTemp(os.TempDir(), "hooshang-voice-*")
	if err != nil {
		return "", fmt.Errorf("temp dir: %w", err)
	}
	defer os.RemoveAll(dir)
	wav, err := w.toWAV(ctx, dir, audio, mime)
	if err != nil {
		return "", err
	}
	return w.recognize(ctx, wav)
}

// toWAV writes the audio into dir and converts it to 16 kHz mono 16-bit WAV,
// the format whisper wants. -vn drops the video track of a video note.
func (w *Whisper) toWAV(ctx context.Context, dir string, audio []byte, mime string) (string, error) {
	in := filepath.Join(dir, "in"+extension(mime))
	if err := os.WriteFile(in, audio, 0o600); err != nil {
		return "", fmt.Errorf("write audio: %w", err)
	}
	wav := filepath.Join(dir, "out.wav")
	var stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, w.ffmpeg, "-nostdin", "-loglevel", "error", "-y", "-i", in,
		"-vn", "-ar", "16000", "-ac", "1", "-c:a", "pcm_s16le", wav)
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("ffmpeg: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	return wav, nil
}

// recognize runs whisper-cli on a WAV file. -nt drops the timestamps and -np
// everything but the text; -sns keeps it from writing sounds as text.
func (w *Whisper) recognize(ctx context.Context, wav string) (string, error) {
	args := []string{"-m", w.opts.Model, "-l", w.opts.Language, "-nt", "-np", "-sns", "-f", wav}
	if w.opts.Threads > 0 {
		args = append(args, "-t", strconv.Itoa(w.opts.Threads))
	}
	var stdout, stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, w.whisper, args...)
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("whisper-cli: %w: %s", err, lastLine(stderr.String()))
	}
	return Clean(stdout.String(), 0), nil
}

// lastLine is the last non-empty line of whisper's log, where its error is.
func lastLine(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	return strings.TrimSpace(lines[len(lines)-1])
}

// extension is the file extension for a MIME type (ffmpeg sniffs the content,
// so an unknown type is harmless).
func extension(mime string) string {
	switch strings.ToLower(strings.TrimSpace(mime)) {
	case "audio/ogg", "audio/opus", "application/ogg":
		return ".ogg"
	case "audio/mpeg", "audio/mp3":
		return ".mp3"
	case "audio/mp4", "audio/x-m4a", "audio/aac":
		return ".m4a"
	case "video/mp4":
		return ".mp4"
	case "audio/wav", "audio/x-wav":
		return ".wav"
	}
	return ".bin"
}

// annotation matches the sound descriptions whisper writes in brackets:
// [Music], [BLANK_AUDIO], [موسیقی].
var annotation = regexp.MustCompile(`\[[^\]]*\]`)

// Clean makes whisper's output one line: annotations dropped, whitespace
// collapsed, and cut to maxWords words (with "…") when maxWords > 0.
func Clean(s string, maxWords int) string {
	words := strings.Fields(annotation.ReplaceAllString(s, " "))
	if maxWords > 0 && len(words) > maxWords {
		return strings.Join(words[:maxWords], " ") + "…"
	}
	return strings.Join(words, " ")
}
