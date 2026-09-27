// Package sticker turns images, animations and videos into WhatsApp stickers:
// a 512x512 WebP with the source centered on a transparent canvas, optional
// meme text, and the pack metadata WhatsApp shows when a sticker is saved.
// It knows nothing about WhatsApp transport; commands hand it bytes and send
// what it returns.
package sticker

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	"image/gif"
	_ "image/jpeg" // registers the JPEG decoder for image.Decode
	_ "image/png"  // registers the PNG decoder for image.Decode

	"github.com/gen2brain/webp"
	"golang.org/x/image/draw"
)

const (
	// Size is the width and height of every sticker WhatsApp renders.
	Size = 512
	// MaxBytes is the largest sticker this package produces.
	MaxBytes = 1 << 20
	// MaxFrames bounds the memory an animation may use (each frame is 1 MiB).
	MaxFrames = 120
	// PackName and Emoji are written into every sticker's metadata.
	PackName = "WazzapAgent"
	Emoji    = "🤖"
)

// ErrUnsupported means the input is not an image this package can read.
var ErrUnsupported = errors.New("unsupported image format")

// Text is optional meme text drawn at the top and bottom of the sticker.
type Text struct {
	Top    string
	Bottom string
}

// Result is an encoded sticker.
type Result struct {
	WebP     []byte
	Animated bool
}

// FromImage makes a sticker from a JPEG, PNG, GIF or WebP. An animated GIF or
// WebP stays animated.
func FromImage(data []byte, text Text) (Result, error) {
	frames, err := decodeFrames(data)
	if err != nil {
		return Result{}, err
	}
	return encodeFrames(frames, text)
}

// FromVideo makes an animated sticker from a video, using ffmpeg to read up
// to MaxVideoSeconds of it. It returns ErrFFmpegMissing when ffmpeg is not on
// PATH.
func FromVideo(ctx context.Context, data []byte, text Text) (Result, error) {
	frames, err := videoFrames(ctx, data)
	if err != nil {
		return Result{}, err
	}
	return encodeFrames(frames, text)
}

// animation is decoded frames with per-frame delays in milliseconds. One frame
// means a still image.
type animation struct {
	frames []image.Image
	delays []int
	// area is where the picture sits in frames that are already sticker
	// sized and padded; empty means the frames still need fitting.
	area image.Rectangle
}

func decodeFrames(data []byte) (animation, error) {
	switch {
	case isWebP(data):
		decoded, err := webp.DecodeAll(bytes.NewReader(data))
		if err != nil {
			return animation{}, fmt.Errorf("decode webp: %w", err)
		}
		if len(decoded.Image) == 0 {
			return animation{}, ErrUnsupported
		}
		return animation{frames: decoded.Image, delays: decoded.Delay}, nil
	case bytes.HasPrefix(data, []byte("GIF8")):
		decoded, err := gif.DecodeAll(bytes.NewReader(data))
		if err != nil {
			return animation{}, fmt.Errorf("decode gif: %w", err)
		}
		return flattenGIF(decoded), nil
	default:
		decoded, _, err := image.Decode(bytes.NewReader(data))
		if errors.Is(err, image.ErrFormat) {
			return animation{}, ErrUnsupported
		}
		if err != nil {
			return animation{}, fmt.Errorf("decode image: %w", err)
		}
		return animation{frames: []image.Image{decoded}}, nil
	}
}

func isWebP(data []byte) bool {
	return len(data) >= 12 && string(data[0:4]) == "RIFF" && string(data[8:12]) == "WEBP"
}

// flattenGIF draws each GIF frame over the previous ones, since GIF frames
// only hold the pixels that changed.
func flattenGIF(decoded *gif.GIF) animation {
	bounds := image.Rect(0, 0, decoded.Config.Width, decoded.Config.Height)
	if bounds.Empty() && len(decoded.Image) > 0 {
		bounds = decoded.Image[0].Bounds()
	}
	canvas := image.NewRGBA(bounds)
	result := animation{}
	for index, frame := range decoded.Image {
		if index >= MaxFrames {
			break
		}
		previous := image.NewRGBA(bounds)
		draw.Copy(previous, image.Point{}, canvas, bounds, draw.Src, nil)
		draw.Draw(canvas, frame.Bounds(), frame, frame.Bounds().Min, draw.Over)
		snapshot := image.NewRGBA(bounds)
		draw.Copy(snapshot, image.Point{}, canvas, bounds, draw.Src, nil)
		result.frames = append(result.frames, snapshot)
		result.delays = append(result.delays, decoded.Delay[index]*10)
		if index < len(decoded.Disposal) {
			switch decoded.Disposal[index] {
			case gif.DisposalBackground:
				draw.Draw(canvas, frame.Bounds(), image.Transparent, image.Point{}, draw.Src)
			case gif.DisposalPrevious:
				canvas = previous
			}
		}
	}
	return result
}

// encodeFrames fits every frame onto the sticker canvas, draws the text, and
// encodes the WebP at the best quality that stays under MaxBytes.
func encodeFrames(input animation, text Text) (Result, error) {
	if len(input.frames) == 0 {
		return Result{}, ErrUnsupported
	}
	if len(input.frames) > MaxFrames {
		input.frames, input.delays = input.frames[:MaxFrames], input.delays[:MaxFrames]
	}
	area := input.area
	if area.Empty() {
		area = fitRect(input.frames[0].Bounds())
	}
	overlay, err := renderText(text, area)
	if err != nil {
		return Result{}, err
	}
	animated := len(input.frames) > 1
	scaler := draw.Interpolator(draw.CatmullRom)
	if animated {
		scaler = draw.ApproxBiLinear
	}
	canvases := make([]image.Image, len(input.frames))
	for index, frame := range input.frames {
		canvas := image.NewRGBA(image.Rect(0, 0, Size, Size))
		scaler.Scale(canvas, fitRect(frame.Bounds()), frame, frame.Bounds(), draw.Src, nil)
		if overlay != nil {
			draw.Draw(canvas, canvas.Bounds(), overlay, image.Point{}, draw.Over)
		}
		canvases[index] = canvas
	}

	if !animated {
		for _, quality := range []int{90, 75, 50, 30} {
			var encoded bytes.Buffer
			if err := webp.Encode(&encoded, canvases[0], webp.Options{Quality: quality, Method: 4}); err != nil {
				return Result{}, fmt.Errorf("encode webp: %w", err)
			}
			if encoded.Len() <= MaxBytes {
				return finish(encoded.Bytes(), false)
			}
		}
		return Result{}, errors.New("sticker is still too large at the lowest quality")
	}

	// Each attempt lowers quality and keeps every Nth frame, stretching its
	// delay so the animation plays at the same speed.
	for _, attempt := range []struct{ quality, step int }{{75, 1}, {50, 2}, {30, 3}} {
		anim := &webp.WEBP{}
		for index := 0; index < len(canvases); index += attempt.step {
			delay := 0
			for offset := 0; offset < attempt.step && index+offset < len(canvases); offset++ {
				delay += frameDelay(input.delays, index+offset)
			}
			anim.Image = append(anim.Image, canvases[index])
			anim.Delay = append(anim.Delay, delay)
		}
		var encoded bytes.Buffer
		if err := webp.EncodeAll(&encoded, anim, webp.Options{Quality: attempt.quality, Method: 4}); err != nil {
			return Result{}, fmt.Errorf("encode animated webp: %w", err)
		}
		if encoded.Len() <= MaxBytes {
			return finish(encoded.Bytes(), true)
		}
	}
	return Result{}, errors.New("animated sticker is still too large at the lowest quality")
}

func finish(encoded []byte, animated bool) (Result, error) {
	withMetadata, err := WithMetadata(encoded, PackName, Emoji)
	if err != nil {
		return Result{}, err
	}
	return Result{WebP: withMetadata, Animated: animated}, nil
}

// frameDelay defaults missing or zero delays to 100 ms, as browsers do.
func frameDelay(delays []int, index int) int {
	if index < len(delays) && delays[index] > 0 {
		return delays[index]
	}
	return 100
}

// fitRect is where a source of this size lands on the canvas: scaled to fit,
// aspect ratio kept, centered.
func fitRect(source image.Rectangle) image.Rectangle {
	width, height := source.Dx(), source.Dy()
	if width <= 0 || height <= 0 {
		return image.Rect(0, 0, Size, Size)
	}
	scaledWidth, scaledHeight := Size, Size
	if width > height {
		scaledHeight = max(1, height*Size/width)
	} else {
		scaledWidth = max(1, width*Size/height)
	}
	left, top := (Size-scaledWidth)/2, (Size-scaledHeight)/2
	return image.Rect(left, top, left+scaledWidth, top+scaledHeight)
}

// Make makes a sticker from a video when video is set, else from an image.
func Make(ctx context.Context, data []byte, video bool, text Text) (Result, error) {
	if video {
		return FromVideo(ctx, data, text)
	}
	return FromImage(data, text)
}

// Problem explains a Make error in words for the person who sent the file.
func Problem(err error) string {
	switch {
	case errors.Is(err, ErrFFmpegMissing):
		return "Making stickers from videos and GIFs needs ffmpeg installed on the computer running the bot."
	case errors.Is(err, ErrUnsupported):
		return "That file is not an image this bot can read."
	default:
		return "Could not make a sticker from that file: " + err.Error()
	}
}
