package sticker

import (
	_ "embed"
	"fmt"
	"image"
	"image/color"
	"math"
	"strings"
	"sync"

	"golang.org/x/image/font"
	"golang.org/x/image/font/opentype"
	"golang.org/x/image/math/fixed"
)

// Anton is an Impact-style face under the SIL Open Font License
// (fonts/Anton-OFL.txt), so meme text looks the same on every host.
//
//go:embed fonts/Anton-Regular.ttf
var antonTTF []byte

var (
	memeFontOnce sync.Once
	memeFont     *opentype.Font
	memeFontErr  error
)

func loadMemeFont() (*opentype.Font, error) {
	memeFontOnce.Do(func() {
		memeFont, memeFontErr = opentype.Parse(antonTTF)
	})
	return memeFont, memeFontErr
}

// renderText draws the top and bottom text inside area, the part of the
// canvas the image covers, as white letters with a black outline. It returns
// nil when there is no text.
func renderText(text Text, area image.Rectangle) (*image.RGBA, error) {
	top, bottom := strings.ToUpper(strings.TrimSpace(text.Top)), strings.ToUpper(strings.TrimSpace(text.Bottom))
	if top == "" && bottom == "" {
		return nil, nil
	}
	parsed, err := loadMemeFont()
	if err != nil {
		return nil, fmt.Errorf("load meme font: %w", err)
	}
	overlay := image.NewRGBA(image.Rect(0, 0, Size, Size))
	padding := max(4, min(area.Dx(), area.Dy())/25)
	// Each block may use up to 40% of the image height (a lone block gets
	// more room), so top and bottom text never overlap.
	share := 0.4
	if top == "" || bottom == "" {
		share = 0.8
	}
	maxBlockHeight := int(float64(area.Dy()-2*padding) * share)
	maxWidth := area.Dx() - 2*padding
	for _, block := range []struct {
		text  string
		atTop bool
	}{{top, true}, {bottom, false}} {
		if block.text == "" {
			continue
		}
		face, lines, lineHeight, err := layoutText(parsed, block.text, maxWidth, maxBlockHeight)
		if err != nil {
			return nil, err
		}
		y := area.Min.Y + padding
		if !block.atTop {
			y = area.Max.Y - padding - lineHeight*len(lines)
		}
		for _, line := range lines {
			drawOutlined(overlay, face, line, area.Min.X+area.Dx()/2, y+lineHeight)
			y += lineHeight
		}
		_ = face.Close()
	}
	return overlay, nil
}

// layoutText picks the largest font size, from about a tenth of the sticker
// down, at which the words wrap into lines that fit the width and height.
func layoutText(parsed *opentype.Font, text string, maxWidth, maxHeight int) (font.Face, []string, int, error) {
	const largest, smallest = 64, 16
	for size := largest; ; size -= 2 {
		face, err := opentype.NewFace(parsed, &opentype.FaceOptions{Size: float64(size), DPI: 72, Hinting: font.HintingFull})
		if err != nil {
			return nil, nil, 0, fmt.Errorf("create meme font face: %w", err)
		}
		lines, fits := wrapText(face, text, maxWidth)
		lineHeight := int(math.Ceil(float64(size) * 1.1))
		if (fits && lineHeight*len(lines) <= maxHeight) || size <= smallest {
			return face, lines, lineHeight, nil
		}
		_ = face.Close()
	}
}

// wrapText breaks text into lines no wider than maxWidth. fits is false when a
// single word is wider than maxWidth on its own.
func wrapText(face font.Face, text string, maxWidth int) ([]string, bool) {
	limit := fixed.I(maxWidth)
	fits := true
	var lines []string
	current := ""
	for _, word := range strings.Fields(text) {
		if font.MeasureString(face, word) > limit {
			fits = false
		}
		candidate := word
		if current != "" {
			candidate = current + " " + word
		}
		if current != "" && font.MeasureString(face, candidate) > limit {
			lines = append(lines, current)
			current = word
			continue
		}
		current = candidate
	}
	if current != "" {
		lines = append(lines, current)
	}
	return lines, fits
}

// drawOutlined draws line centered on centerX with its baseline at baseline:
// a black outline made of offset copies, then the white letters.
func drawOutlined(dst *image.RGBA, face font.Face, line string, centerX, baseline int) {
	width := font.MeasureString(face, line)
	origin := fixed.Point26_6{X: fixed.I(centerX) - width/2, Y: fixed.I(baseline - face.Metrics().Descent.Ceil())}
	radius := max(2, face.Metrics().Height.Ceil()/14)
	drawer := &font.Drawer{Dst: dst, Src: image.NewUniform(color.Black), Face: face}
	for angle := 0.0; angle < 2*math.Pi; angle += math.Pi / 8 {
		drawer.Dot = origin.Add(fixed.P(int(math.Round(float64(radius)*math.Cos(angle))), int(math.Round(float64(radius)*math.Sin(angle)))))
		drawer.DrawString(line)
	}
	drawer.Src = image.NewUniform(color.White)
	drawer.Dot = origin
	drawer.DrawString(line)
}
