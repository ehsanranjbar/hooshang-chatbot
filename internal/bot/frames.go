package bot

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/ehsanranjbar/hooshang/internal/llm"
)

// maxClipBytes is Telegram's bot API download limit.
const maxClipBytes = 20 << 20

// frameSide is the longest side of an extracted frame, in pixels.
const frameSide = 512

// ffmpegTimeout bounds one frame extraction (probe included).
const ffmpegTimeout = 30 * time.Second

// ffmpegTools resolves ffmpeg and ffprobe once (features.media.ffmpeg, else PATH).
// Missing tools are not an error: clips then fall back to their thumbnail.
func (b *Bot) ffmpegTools() (ffmpeg, ffprobe string) {
	b.ffOnce.Do(func() {
		name := b.deps.Cfg.FFmpeg()
		if name == "" {
			name = "ffmpeg"
		}
		p, err := exec.LookPath(name)
		if err != nil {
			b.logger.Debug("ffmpeg not found: animated stickers and GIFs use their thumbnail", "err", err)
			return
		}
		b.ffmpeg = p
		// ffprobe ships with ffmpeg: prefer the one next to it.
		if q, err := exec.LookPath(filepath.Join(filepath.Dir(p), "ffprobe")); err == nil {
			b.ffprobe = q
		} else if q, err := exec.LookPath("ffprobe"); err == nil {
			b.ffprobe = q
		}
	})
	return b.ffmpeg, b.ffprobe
}

// mediaImages returns the pictures to send for a media ref: n frames of the
// clip when it has one, ffmpeg can decode it and features.media.frames > 1, else the
// single downloaded image (the thumbnail for clips). Extraction never fails
// the caller: any problem falls back to the thumbnail. framed says whether
// the images are clip frames.
func (b *Bot) mediaImages(ctx context.Context, ref mediaRef) (imgs []llm.Image, framed bool, err error) {
	if n := b.deps.Cfg.Features.Media.Frames; ref.clipID != "" && n > 1 {
		frames, ferr := b.clipFrames(ctx, ref.clipID, n)
		if ferr == nil {
			return frames, true, nil
		}
		b.logger.Debug("frame extraction failed: using the thumbnail", "kind", ref.kind, "err", ferr)
	}
	img, err := b.downloadImage(ctx, ref.fileID)
	if err != nil {
		return nil, false, err
	}
	return []llm.Image{img}, false, nil
}

// clipFrames downloads a video sticker or GIF and extracts n frames from it.
func (b *Bot) clipFrames(ctx context.Context, clipID string, n int) ([]llm.Image, error) {
	ffmpeg, ffprobe := b.ffmpegTools()
	if ffmpeg == "" || ffprobe == "" {
		return nil, fmt.Errorf("ffmpeg or ffprobe not installed")
	}
	data, err := b.downloadFile(ctx, clipID, maxClipBytes)
	if err != nil {
		return nil, err
	}
	return extractFrames(ctx, ffmpeg, ffprobe, data, n)
}

// extractFrames writes the clip to a temp file and returns n PNG frames
// spread evenly over it, scaled to at most frameSide px on the longer side.
//
// Why ffprobe + one ffmpeg run: the select filter can pick frames by
// timestamp in a single decoding pass, but it needs the duration to know where
// to look; ffprobe reads it from the container header in milliseconds. A clip
// whose duration it cannot tell (N/A) is not worth guessing at: the caller
// falls back to the thumbnail. The frames are taken at the centres of n equal
// slices of the clip, (k+½)·d/n, so the last one is never past the final frame
// (aiming at exactly d would often select nothing).
func extractFrames(ctx context.Context, ffmpeg, ffprobe string, clip []byte, n int) ([]llm.Image, error) {
	ctx, cancel := context.WithTimeout(ctx, ffmpegTimeout)
	defer cancel()

	f, err := os.CreateTemp(os.TempDir(), "hooshang-clip-*")
	if err != nil {
		return nil, fmt.Errorf("temp clip: %w", err)
	}
	defer os.Remove(f.Name())
	_, werr := f.Write(clip)
	if cerr := f.Close(); werr == nil {
		werr = cerr
	}
	if werr != nil {
		return nil, fmt.Errorf("write clip: %w", werr)
	}
	dir, err := os.MkdirTemp(os.TempDir(), "hooshang-frames-*")
	if err != nil {
		return nil, fmt.Errorf("temp frames: %w", err)
	}
	defer os.RemoveAll(dir)

	out, err := exec.CommandContext(ctx, ffprobe, "-v", "error", "-show_entries", "format=duration",
		"-of", "default=noprint_wrappers=1:nokey=1", f.Name()).Output()
	if err != nil {
		return nil, fmt.Errorf("ffprobe: %w", err)
	}
	dur, err := strconv.ParseFloat(strings.TrimSpace(string(out)), 64)
	if err != nil || dur <= 0 {
		return nil, fmt.Errorf("ffprobe: no duration (%q)", strings.TrimSpace(string(out)))
	}

	// selected_n counts the frames chosen so far: the next one is the first
	// whose timestamp reaches the middle of the next slice.
	slice := dur / float64(n)
	vf := fmt.Sprintf("select='gte(t,(selected_n+0.5)*%.6f)',scale='min(%d,iw)':'min(%d,ih)':force_original_aspect_ratio=decrease:force_divisible_by=2",
		slice, frameSide, frameSide)
	var stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, ffmpeg, "-nostdin", "-loglevel", "error", "-y", "-i", f.Name(),
		"-an", "-vf", vf, "-fps_mode", "passthrough", "-frames:v", strconv.Itoa(n),
		filepath.Join(dir, "f%02d.png"))
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("ffmpeg: %w: %s", err, strings.TrimSpace(stderr.String()))
	}

	names, err := filepath.Glob(filepath.Join(dir, "f*.png"))
	if err != nil {
		return nil, err
	}
	sort.Strings(names) // f01, f02, …: in clip order
	var frames []llm.Image
	for _, name := range names {
		data, err := os.ReadFile(name)
		if err != nil {
			return nil, fmt.Errorf("read frame: %w", err)
		}
		frames = append(frames, llm.Image{MIME: "image/png", Data: data})
	}
	if len(frames) < 2 {
		return nil, fmt.Errorf("ffmpeg gave %d frames", len(frames))
	}
	return frames, nil
}
