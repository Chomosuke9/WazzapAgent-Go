package sticker

import (
	"bytes"
	"image"
	"image/color"
	"image/gif"
	"image/png"
	"testing"

	"github.com/gen2brain/webp"
)

func testPNG(t *testing.T, width, height int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, width, height))
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			img.Set(x, y, color.RGBA{R: uint8(x), G: uint8(y), B: 128, A: 255})
		}
	}
	var encoded bytes.Buffer
	if err := png.Encode(&encoded, img); err != nil {
		t.Fatal(err)
	}
	return encoded.Bytes()
}

func TestFromImageMakesSquareStickerWithMetadata(t *testing.T) {
	result, err := FromImage(testPNG(t, 800, 400), Text{Top: "so me", Bottom: "when monday arrives and the coffee machine is broken"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Animated || len(result.WebP) == 0 || len(result.WebP) > MaxBytes {
		t.Fatalf("result animated=%v size=%d", result.Animated, len(result.WebP))
	}
	decoded, err := webp.Decode(bytes.NewReader(result.WebP))
	if err != nil {
		t.Fatalf("decode sticker: %v", err)
	}
	if decoded.Bounds().Dx() != Size || decoded.Bounds().Dy() != Size {
		t.Fatalf("sticker is %v, want %dx%d", decoded.Bounds(), Size, Size)
	}
	// A wide image is letterboxed: the top rows stay transparent.
	if _, _, _, alpha := decoded.At(Size/2, 2).RGBA(); alpha != 0 {
		t.Fatalf("letterbox is not transparent, alpha=%d", alpha)
	}
	if !bytes.Contains(result.WebP, []byte(`"sticker-pack-name":"`+PackName+`"`)) {
		t.Fatal("sticker has no pack metadata")
	}
}

func TestFromImageKeepsGIFAnimation(t *testing.T) {
	palette := color.Palette{color.Black, color.White}
	animated := &gif.GIF{}
	for index := 0; index < 3; index++ {
		frame := image.NewPaletted(image.Rect(0, 0, 64, 64), palette)
		frame.SetColorIndex(index*10, index*10, 1)
		animated.Image = append(animated.Image, frame)
		animated.Delay = append(animated.Delay, 10)
	}
	var encoded bytes.Buffer
	if err := gif.EncodeAll(&encoded, animated); err != nil {
		t.Fatal(err)
	}
	result, err := FromImage(encoded.Bytes(), Text{})
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := webp.DecodeAll(bytes.NewReader(result.WebP))
	if err != nil {
		t.Fatal(err)
	}
	if !result.Animated || len(decoded.Image) != 3 {
		t.Fatalf("animated=%v frames=%d", result.Animated, len(decoded.Image))
	}
}

func TestFromImageRejectsUnknownData(t *testing.T) {
	if _, err := FromImage([]byte("not an image"), Text{}); err != ErrUnsupported {
		t.Fatalf("err = %v, want ErrUnsupported", err)
	}
}

func TestWithMetadataReplacesExistingMetadata(t *testing.T) {
	result, err := FromImage(testPNG(t, 64, 64), Text{})
	if err != nil {
		t.Fatal(err)
	}
	again, err := WithMetadata(result.WebP, "Other", "🙂")
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Count(again, []byte("EXIF")) != 1 || !bytes.Contains(again, []byte(`"Other"`)) {
		t.Fatal("metadata was not replaced")
	}
	if _, err := webp.Decode(bytes.NewReader(again)); err != nil {
		t.Fatalf("decode after metadata: %v", err)
	}
}

func TestFitRectCentersAndKeepsAspect(t *testing.T) {
	got := fitRect(image.Rect(0, 0, 1000, 500))
	if got != image.Rect(0, 128, 512, 384) {
		t.Fatalf("fitRect = %v", got)
	}
}

func testGIF(t *testing.T, frames, size int) []byte {
	t.Helper()
	palette := color.Palette{color.Black, color.White}
	animated := &gif.GIF{}
	for index := 0; index < frames; index++ {
		frame := image.NewPaletted(image.Rect(0, 0, size, size), palette)
		frame.SetColorIndex(index%size, 0, 1)
		animated.Image = append(animated.Image, frame)
		animated.Delay = append(animated.Delay, 5)
	}
	var encoded bytes.Buffer
	if err := gif.EncodeAll(&encoded, animated); err != nil {
		t.Fatal(err)
	}
	return encoded.Bytes()
}

func TestCountGIFFramesReadsOnlyTheBlockStructure(t *testing.T) {
	for _, frames := range []int{1, 3, 17} {
		if got := countGIFFrames(testGIF(t, frames, 16)); got != frames {
			t.Errorf("countGIFFrames = %d, want %d", got, frames)
		}
	}
}

func TestHugeAnimationsAreRefusedBeforeDecoding(t *testing.T) {
	// 600 frames of 256x256 compress to little but decode to 39M pixels.
	if _, err := FromImage(testGIF(t, 600, 256), Text{}); err != ErrTooLarge {
		t.Fatalf("GIF err = %v, want ErrTooLarge", err)
	}
	animated, err := FromImage(testGIF(t, 3, 64), Text{})
	if err != nil {
		t.Fatal(err)
	}
	if err := checkDecodedSize(animated.WebP); err != nil {
		t.Fatalf("small animated WebP refused: %v", err)
	}
}

func TestTooManyTinyFramesAreRefused(t *testing.T) {
	if _, err := FromImage(testGIF(t, maxSourceFrames+1, 1), Text{}); err != ErrTooLarge {
		t.Fatalf("err = %v, want ErrTooLarge", err)
	}
}
