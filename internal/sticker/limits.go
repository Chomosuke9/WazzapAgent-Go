package sticker

import (
	"bytes"
	"image"
	"image/gif"
)

const (
	// maxStillPixels bounds a still image's decoded size (100 MiB as RGBA).
	maxStillPixels = 25 << 20
	// maxAnimationPixels bounds all frames of an animation together, since
	// DecodeAll keeps every frame before any are dropped.
	maxAnimationPixels = 32 << 20
)

// checkDecodedSize reads only headers to refuse input whose decoded frames
// would not fit in memory: a few compressed kilobytes can describe
// thousands of large frames.
func checkDecodedSize(data []byte) error {
	width, height, frames := 0, 0, 1
	switch {
	case isWebP(data):
		chunks, err := riffChunks(data[12:])
		if err != nil {
			return ErrUnsupported
		}
		frames = 0
		for _, chunk := range chunks {
			switch chunk.id {
			case "VP8X":
				if len(chunk.data) >= 10 {
					width = int(uint32(chunk.data[4])|uint32(chunk.data[5])<<8|uint32(chunk.data[6])<<16) + 1
					height = int(uint32(chunk.data[7])|uint32(chunk.data[8])<<8|uint32(chunk.data[9])<<16) + 1
				}
			case "ANMF":
				frames++
			}
		}
		frames = max(frames, 1)
		if width == 0 {
			config, _, err := image.DecodeConfig(bytes.NewReader(data))
			if err != nil {
				return nil // let the decoder report it
			}
			width, height = config.Width, config.Height
		}
	case bytes.HasPrefix(data, []byte("GIF8")):
		config, err := gif.DecodeConfig(bytes.NewReader(data))
		if err != nil {
			return nil
		}
		width, height, frames = config.Width, config.Height, countGIFFrames(data)
	default:
		config, _, err := image.DecodeConfig(bytes.NewReader(data))
		if err != nil {
			return nil
		}
		width, height = config.Width, config.Height
	}
	pixels := int64(width) * int64(height)
	if pixels > maxStillPixels || (frames > 1 && pixels*int64(frames) > maxAnimationPixels) {
		return ErrTooLarge
	}
	return nil
}

// countGIFFrames counts image descriptors by walking the GIF block structure
// without decompressing anything. A malformed stream counts what it reached.
func countGIFFrames(data []byte) int {
	if len(data) < 13 {
		return 0
	}
	position := 13
	if flags := data[10]; flags&0x80 != 0 {
		position += 3 << ((flags & 0x07) + 1) // global color table
	}
	skipSubBlocks := func() bool {
		for position < len(data) {
			size := int(data[position])
			position++
			if size == 0 {
				return true
			}
			position += size
		}
		return false
	}
	frames := 0
	for position < len(data) {
		switch data[position] {
		case 0x21: // extension: label, then sub-blocks
			position += 2
			if !skipSubBlocks() {
				return frames
			}
		case 0x2C: // image descriptor
			if position+10 > len(data) {
				return frames
			}
			frames++
			flags := data[position+9]
			position += 10
			if flags&0x80 != 0 {
				position += 3 << ((flags & 0x07) + 1) // local color table
			}
			position++ // LZW minimum code size
			if !skipSubBlocks() {
				return frames
			}
		default: // 0x3B trailer, or garbage
			return frames
		}
	}
	return frames
}
