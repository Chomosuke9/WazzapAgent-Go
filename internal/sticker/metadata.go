package sticker

import (
	"encoding/binary"
	"encoding/json"
	"errors"

	"github.com/google/uuid"
)

// WithMetadata returns webp with an EXIF chunk that names the sticker pack
// and its emoji, the metadata WhatsApp reads when someone saves the sticker.
// Any EXIF chunk already present is replaced.
func WithMetadata(webp []byte, packName, emoji string) ([]byte, error) {
	if !isWebP(webp) {
		return nil, errors.New("sticker metadata: input is not a WebP file")
	}
	chunks, err := riffChunks(webp[12:])
	if err != nil {
		return nil, err
	}
	payload, err := json.Marshal(map[string]any{
		"sticker-pack-id":   uuid.NewString(),
		"sticker-pack-name": packName,
		"emojis":            []string{emoji},
	})
	if err != nil {
		return nil, err
	}
	// A little-endian TIFF with one IFD entry (tag 0x5741, type UNDEFINED)
	// whose value is the JSON that follows it: the layout WhatsApp expects.
	exif := []byte{
		0x49, 0x49, 0x2A, 0x00, 0x08, 0x00, 0x00, 0x00,
		0x01, 0x00, 0x41, 0x57, 0x07, 0x00, 0x00, 0x00,
		0x00, 0x00, 0x16, 0x00, 0x00, 0x00,
	}
	binary.LittleEndian.PutUint32(exif[14:18], uint32(len(payload)))
	exif = append(exif, payload...)

	kept := make([]riffChunk, 0, len(chunks)+2)
	for _, chunk := range chunks {
		if chunk.id != "EXIF" {
			kept = append(kept, chunk)
		}
	}
	if len(kept) == 0 {
		return nil, errors.New("sticker metadata: WebP has no image data")
	}
	if kept[0].id != "VP8X" {
		// A simple (single-chunk) WebP cannot carry EXIF; wrap it in the
		// extended format, whose header records the canvas size.
		header, err := extendedHeader(kept[0])
		if err != nil {
			return nil, err
		}
		kept = append([]riffChunk{header}, kept...)
	}
	kept[0].data = append([]byte(nil), kept[0].data...)
	kept[0].data[0] |= 0x08 // EXIF metadata present
	kept = append(kept, riffChunk{id: "EXIF", data: exif})

	body := []byte("WEBP")
	for _, chunk := range kept {
		body = append(body, chunk.id...)
		body = binary.LittleEndian.AppendUint32(body, uint32(len(chunk.data)))
		body = append(body, chunk.data...)
		if len(chunk.data)%2 == 1 {
			body = append(body, 0)
		}
	}
	out := []byte("RIFF")
	out = binary.LittleEndian.AppendUint32(out, uint32(len(body)))
	return append(out, body...), nil
}

type riffChunk struct {
	id   string
	data []byte
}

func riffChunks(data []byte) ([]riffChunk, error) {
	var chunks []riffChunk
	for len(data) > 0 {
		if len(data) < 8 {
			return nil, errors.New("sticker metadata: truncated WebP chunk header")
		}
		size := int(binary.LittleEndian.Uint32(data[4:8]))
		if size < 0 || size > len(data)-8 {
			return nil, errors.New("sticker metadata: truncated WebP chunk")
		}
		chunks = append(chunks, riffChunk{id: string(data[:4]), data: data[8 : 8+size]})
		data = data[8+size:]
		if size%2 == 1 && len(data) > 0 {
			data = data[1:]
		}
	}
	return chunks, nil
}

// extendedHeader builds the VP8X chunk for a simple lossy (VP8) or lossless
// (VP8L) WebP.
func extendedHeader(image riffChunk) (riffChunk, error) {
	var width, height int
	var flags byte
	switch image.id {
	case "VP8 ":
		// Frame tag (3 bytes), start code 9d 01 2a, then 14-bit sizes.
		if len(image.data) < 10 || image.data[3] != 0x9d || image.data[4] != 0x01 || image.data[5] != 0x2a {
			return riffChunk{}, errors.New("sticker metadata: invalid VP8 header")
		}
		width = int(binary.LittleEndian.Uint16(image.data[6:8]) & 0x3fff)
		height = int(binary.LittleEndian.Uint16(image.data[8:10]) & 0x3fff)
	case "VP8L":
		// Signature 0x2f, then 14-bit width-1, 14-bit height-1, 1-bit alpha.
		if len(image.data) < 5 || image.data[0] != 0x2f {
			return riffChunk{}, errors.New("sticker metadata: invalid VP8L header")
		}
		bits := binary.LittleEndian.Uint32(image.data[1:5])
		width = int(bits&0x3fff) + 1
		height = int((bits>>14)&0x3fff) + 1
		if bits&(1<<28) != 0 {
			flags |= 0x10 // alpha
		}
	default:
		return riffChunk{}, errors.New("sticker metadata: unknown WebP image chunk " + image.id)
	}
	data := make([]byte, 10)
	data[0] = flags
	putUint24(data[4:7], uint32(width-1))
	putUint24(data[7:10], uint32(height-1))
	return riffChunk{id: "VP8X", data: data}, nil
}

func putUint24(dst []byte, value uint32) {
	dst[0], dst[1], dst[2] = byte(value), byte(value>>8), byte(value>>16)
}
