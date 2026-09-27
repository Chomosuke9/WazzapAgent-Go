package sticker

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"image"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
)

const (
	// MaxVideoSeconds is how much of a video becomes the sticker.
	MaxVideoSeconds = 6
	videoFPS        = 12
)

// ErrFFmpegMissing means a video was given but ffmpeg is not installed.
var ErrFFmpegMissing = errors.New("ffmpeg is not installed")

// videoFrames decodes the start of a video into sticker-sized RGBA frames.
// ffmpeg scales and pads each frame to Size x Size and writes raw pixels, so
// any ffmpeg build works; no WebP support is needed in ffmpeg itself.
func videoFrames(ctx context.Context, data []byte) (animation, error) {
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		return animation{}, ErrFFmpegMissing
	}
	// MP4 files often keep their index at the end, so ffmpeg needs a
	// seekable file rather than a pipe.
	input, err := os.CreateTemp("", "wazzapagent-sticker-*")
	if err != nil {
		return animation{}, fmt.Errorf("create video temp file: %w", err)
	}
	defer os.Remove(input.Name())
	if _, err := input.Write(data); err != nil {
		_ = input.Close()
		return animation{}, fmt.Errorf("write video temp file: %w", err)
	}
	if err := input.Close(); err != nil {
		return animation{}, fmt.Errorf("write video temp file: %w", err)
	}

	size := strconv.Itoa(Size)
	filter := "fps=" + strconv.Itoa(videoFPS) +
		",scale=" + size + ":" + size + ":force_original_aspect_ratio=decrease" +
		",format=rgba,pad=" + size + ":" + size + ":(ow-iw)/2:(oh-ih)/2:color=black@0"
	cmd := exec.CommandContext(ctx, ffmpeg, "-hide_banner", "-loglevel", "error", "-nostdin",
		"-t", strconv.Itoa(MaxVideoSeconds), "-i", input.Name(), "-an", "-vf", filter,
		"-frames:v", strconv.Itoa(MaxFrames), "-f", "rawvideo", "-pix_fmt", "rgba", "pipe:1")
	var stderr strings.Builder
	cmd.Stderr = &stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return animation{}, err
	}
	if err := cmd.Start(); err != nil {
		return animation{}, fmt.Errorf("start ffmpeg: %w", err)
	}
	reader := bufio.NewReaderSize(stdout, 1<<20)
	result := animation{}
	delay := 1000 / videoFPS
	for len(result.frames) < MaxFrames {
		frame := image.NewRGBA(image.Rect(0, 0, Size, Size))
		if _, err := io.ReadFull(reader, frame.Pix); err != nil {
			break
		}
		result.frames = append(result.frames, frame)
		result.delays = append(result.delays, delay)
	}
	_, _ = io.Copy(io.Discard, reader)
	if err := cmd.Wait(); err != nil {
		return animation{}, fmt.Errorf("ffmpeg: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	if len(result.frames) == 0 {
		return animation{}, errors.New("ffmpeg produced no frames")
	}
	result.area = opaqueBounds(result.frames[0].(*image.RGBA))
	return result, nil
}

// opaqueBounds is the smallest rectangle holding every non-transparent pixel:
// the video inside ffmpeg's transparent padding.
func opaqueBounds(frame *image.RGBA) image.Rectangle {
	area := image.Rectangle{}
	bounds := frame.Bounds()
	for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
		for x := bounds.Min.X; x < bounds.Max.X; x++ {
			if frame.RGBAAt(x, y).A != 0 {
				area = area.Union(image.Rect(x, y, x+1, y+1))
			}
		}
	}
	return area
}
