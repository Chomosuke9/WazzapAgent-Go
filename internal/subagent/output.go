package subagent

import (
	"bytes"
	"fmt"
	"mime"
	"os"
	"path/filepath"
	"strings"

	"github.com/Chomosuke9/WazzapAgent-Go/internal/agent"
)

// StagedFile is an output file ready to send to the chat.
type StagedFile struct {
	Path string
	Name string
	Size int64
	Mime string
	// Kind is how WhatsApp shows it: image, video, audio or document.
	Kind string
}

// SkippedFile is an output file that will not be sent, and why.
type SkippedFile struct {
	Name   string
	Reason string
}

// extensionMimes are types Go's mime package does not know on every system.
var extensionMimes = map[string]string{
	".webp": "image/webp",
	".docx": "application/vnd.openxmlformats-officedocument.wordprocessingml.document",
	".xlsx": "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet",
	".pptx": "application/vnd.openxmlformats-officedocument.presentationml.presentation",
	".odt":  "application/vnd.oasis.opendocument.text",
	".ods":  "application/vnd.oasis.opendocument.spreadsheet",
	".odp":  "application/vnd.oasis.opendocument.presentation",
	".rtf":  "application/rtf",
	".7z":   "application/x-7z-compressed",
	".rar":  "application/vnd.rar",
	".tar":  "application/x-tar",
	".gz":   "application/gzip",
	".m4a":  "audio/mp4",
	".opus": "audio/ogg",
	".heic": "image/heic",
	".heif": "image/heif",
	".avif": "image/avif",
	".pdf":  "application/pdf",
	".png":  "image/png",
	".jpg":  "image/jpeg",
	".jpeg": "image/jpeg",
	".gif":  "image/gif",
	".mp4":  "video/mp4",
	".mp3":  "audio/mpeg",
	".ogg":  "audio/ogg",
	".txt":  "text/plain",
	".csv":  "text/csv",
	".json": "application/json",
	".zip":  "application/zip",
	".html": "text/html",
	".md":   "text/markdown",
}

// The types WhatsApp plays inline. Anything else goes as a document.
var (
	whatsAppImageMimes = map[string]bool{"image/jpeg": true, "image/png": true, "image/webp": true}
	whatsAppVideoMimes = map[string]bool{"video/mp4": true}
	whatsAppAudioMimes = map[string]bool{"audio/mpeg": true, "audio/mp4": true, "audio/ogg": true}
)

// sniffMime recognizes common formats by their first bytes, for files whose
// extension is missing or wrong. More specific signatures come first, so a
// WebP is not taken for a generic RIFF file.
func sniffMime(head []byte) string {
	has := func(prefix string) bool { return bytes.HasPrefix(head, []byte(prefix)) }
	riff := func(format string) bool {
		return len(head) >= 12 && string(head[:4]) == "RIFF" && string(head[8:12]) == format
	}
	switch {
	case len(head) == 0:
		return ""
	case has("%PDF-"):
		return "application/pdf"
	case has("\x89PNG\r\n\x1a\n"):
		return "image/png"
	case has("\xff\xd8\xff"):
		return "image/jpeg"
	case has("GIF87a"), has("GIF89a"):
		return "image/gif"
	case riff("WEBP"):
		return "image/webp"
	case riff("WAVE"):
		return "audio/wav"
	case riff("AVI "):
		return "video/x-msvideo"
	case has("OggS"):
		return "audio/ogg"
	case has("fLaC"):
		return "audio/flac"
	case has("ID3"), len(head) >= 2 && head[0] == 0xFF && head[1]&0xE0 == 0xE0:
		return "audio/mpeg"
	case len(head) >= 12 && string(head[4:8]) == "ftyp":
		switch string(head[8:12]) {
		case "M4A ", "M4B ":
			return "audio/mp4"
		case "qt  ":
			return "video/quicktime"
		}
		// The generic ISO brands are used by audio too, but audio-only files
		// nearly always carry an M4A brand.
		return "video/mp4"
	case has("\x1aE\xdf\xa3"):
		return "video/x-matroska"
	case has("PK\x03\x04"), has("PK\x05\x06"), has("PK\x07\x08"):
		return "application/zip"
	case has("\x1f\x8b"):
		return "application/gzip"
	case has("7z\xbc\xaf\x27\x1c"):
		return "application/x-7z-compressed"
	case has("Rar!\x1a\x07"):
		return "application/vnd.rar"
	case has("BM"):
		return "image/bmp"
	case has("{\\rtf"):
		return "application/rtf"
	case has("<!DO"), len(head) >= 5 && strings.EqualFold(string(head[:5]), "<html"):
		return "text/html"
	}
	return ""
}

func readHead(path string, size int) []byte {
	file, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer file.Close()
	head := make([]byte, size)
	read, _ := file.Read(head)
	return head[:read]
}

// animatedWebP reports whether a WebP's VP8X header has the animation flag.
// WhatsApp does not play an animated WebP sent as an image.
func animatedWebP(head []byte) bool {
	return len(head) >= 21 && string(head[:4]) == "RIFF" && string(head[8:12]) == "WEBP" &&
		string(head[12:16]) == "VP8X" && head[20]&0x02 != 0
}

// DetectKind returns how to send the file at path and its MIME type. The
// extension decides; a file with no or an unknown extension is recognized by
// its first bytes, and declared is the service's own claim, used last. A
// sticker is never detected: a bare .webp goes as an image.
func DetectKind(path, name, declared string) (kind, mimeType string) {
	extension := strings.ToLower(filepath.Ext(name))
	mimeType = extensionMimes[extension]
	if mimeType == "" && extension != "" {
		mimeType, _, _ = mime.ParseMediaType(mime.TypeByExtension(extension))
	}
	head := readHead(path, 32)
	if mimeType == "" || mimeType == "application/octet-stream" {
		if sniffed := sniffMime(head); sniffed != "" {
			mimeType = sniffed
		}
	}
	if mimeType == "" || mimeType == "application/octet-stream" {
		if parsed, _, err := mime.ParseMediaType(declared); err == nil && parsed != "" {
			mimeType = parsed
		}
	}
	if mimeType == "" {
		mimeType = "application/octet-stream"
	}
	switch {
	case whatsAppImageMimes[mimeType] && !(mimeType == "image/webp" && animatedWebP(head)):
		return "image", mimeType
	case whatsAppVideoMimes[mimeType]:
		return "video", mimeType
	case whatsAppAudioMimes[mimeType]:
		return "audio", mimeType
	}
	return "document", mimeType
}

// stageOutputs checks each received output file again and decides how to send
// it. A file that is missing, changed or too large is skipped with a reason
// instead of failing the others. Two files with one name get "_1", "_2", ...
func stageOutputs(files []OutputFile) (staged []StagedFile, skipped []SkippedFile) {
	used := make(map[string]bool, len(files))
	for _, file := range files {
		name := safeFileName(file.Name, "unnamed")
		if file.SkipReason != "" || file.Path == "" {
			reason := file.SkipReason
			if reason == "" {
				reason = "empty content"
			}
			skipped = append(skipped, SkippedFile{Name: name, Reason: reason})
			continue
		}
		info, err := os.Lstat(file.Path)
		switch {
		case err != nil:
			skipped = append(skipped, SkippedFile{Name: name, Reason: "file not found"})
			continue
		case !info.Mode().IsRegular():
			skipped = append(skipped, SkippedFile{Name: name, Reason: "not a regular file"})
			continue
		case info.Size() > MaxFileBytes:
			skipped = append(skipped, SkippedFile{Name: name, Reason: fmt.Sprintf("file too large (%s > 200 MB)", formatSize(info.Size()))})
			continue
		case file.Size != info.Size():
			skipped = append(skipped, SkippedFile{Name: name, Reason: "local output metadata is invalid"})
			continue
		}
		final := name
		for counter := 1; used[final]; counter++ {
			extension := filepath.Ext(name)
			final = fmt.Sprintf("%s_%d%s", strings.TrimSuffix(name, extension), counter, extension)
		}
		used[final] = true
		kind, mimeType := DetectKind(file.Path, final, file.Mime)
		staged = append(staged, StagedFile{Path: file.Path, Name: final, Size: info.Size(), Mime: mimeType, Kind: kind})
	}
	return staged, skipped
}

// resultImages reads the staged image files for the model to look at on the
// result turn. An image that is too large or unreadable is left out: the
// model still reads its name in the file list.
func resultImages(staged []StagedFile) []agent.ModelImage {
	var images []agent.ModelImage
	for _, file := range staged {
		if file.Kind != "image" || file.Size > agent.MaxModelImageBytes || len(images) == agent.MaxModelImages {
			continue
		}
		data, err := os.ReadFile(file.Path)
		if err != nil || len(data) == 0 || len(data) > agent.MaxModelImageBytes {
			continue
		}
		images = append(images, agent.ModelImage{Label: "Sub-agent output file " + file.Name + ":", Mime: file.Mime, Data: data})
	}
	return images
}

// safeFileName keeps only the base name of a name that came from outside, so
// "../../etc/x" cannot leave the directory it is written to.
func safeFileName(name, fallback string) string {
	name = strings.TrimSpace(strings.ReplaceAll(name, "\\", "/"))
	name = filepath.Base(name)
	name = strings.Map(func(r rune) rune {
		if r < 32 || r == 127 {
			return -1
		}
		return r
	}, name)
	if name == "" || name == "." || name == ".." || name == "/" {
		return fallback
	}
	if len(name) > 200 {
		extension := filepath.Ext(name)
		if len(extension) > 20 {
			extension = ""
		}
		name = strings.ToValidUTF8(name[:200-len(extension)], "") + extension
	}
	return name
}

func formatSize(size int64) string {
	switch {
	case size < 1024:
		return fmt.Sprintf("%d B", size)
	case size < 1024*1024:
		return fmt.Sprintf("%.1f KB", float64(size)/1024)
	}
	return fmt.Sprintf("%.1f MB", float64(size)/(1024*1024))
}

// formatFileList names the sent and skipped files for the result the model
// reads. It is empty when there is nothing to mention.
func formatFileList(staged []StagedFile, skipped []SkippedFile) string {
	var lines []string
	if len(staged) > 0 {
		lines = append(lines, fmt.Sprintf("Output files attached (%d):", len(staged)))
		for _, file := range staged {
			lines = append(lines, fmt.Sprintf("- %s (%s, %s)", file.Name, file.Kind, formatSize(file.Size)))
		}
	}
	if len(skipped) > 0 {
		if len(lines) > 0 {
			lines = append(lines, "")
		}
		lines = append(lines, fmt.Sprintf("Files skipped (%d):", len(skipped)))
		for _, file := range skipped {
			lines = append(lines, fmt.Sprintf("- %s: %s", file.Name, file.Reason))
		}
	}
	return strings.Join(lines, "\n")
}

// finishedLines is the 【SUBTASK FINISHED】 text: the report, whether the
// task succeeded, the output files, and at most one note. A successful task
// with no file gets a hint, not an order: many tasks only produce a report,
// and the model knows whether it asked for a file.
func finishedLines(report string, completed bool, fileList string, contentDropped, hasFiles bool) string {
	if report == "" {
		report = "No report"
	}
	success := "False"
	if completed {
		success = "True"
	}
	lines := []string{"【SUBTASK FINISHED】", "Result: " + report, "Success: " + success}
	if fileList != "" {
		lines = append(lines, "", fileList)
	}
	switch {
	case contentDropped:
		lines = append(lines, "", "Note: output file(s) could not be delivered because they were too "+
			"large to transfer inline. Tell the user their file could not be sent.")
	case completed && !hasFiles:
		lines = append(lines, "", "Note: the sub-agent did NOT include any file. If you are sure the "+
			"sub-agent should have produced a file for this task, call "+
			"`execute_subtask` AGAIN RIGHT NOW to retry (this is the ONLY turn "+
			"where you can re-dispatch) — otherwise just deliver the report.")
	}
	return strings.Join(lines, "\n")
}

// resultBlock is what the model reads, on the delivering turn only, about
// what it may do with a finished task.
func resultBlock(finished string) string {
	return "## Sub-Agent result for this turn\n" + finished + "\n\n" +
		"The sub-task is FINISHED. This is your ONE chance to act on the " +
		"result — after you reply, this result context is gone. Do NOT say " +
		"\"ok, let me check\" or any pre-task acknowledgement; the work is " +
		"already done.\n\n" +
		"Choose ONE:\n" +
		"1. RESULT IS GOOD → send EXACTLY ONE `reply_message` summarising " +
		"the report for the user, in their language and WhatsApp " +
		"formatting. Output files (if any) are auto-attached after your " +
		"reply; do NOT mention paths or upload them yourself.\n" +
		"2. RESULT IS WRONG / INCOMPLETE / FAILED → call `execute_subtask` " +
		"again now with a revised instruction to fix or retry it. The " +
		"sub-agent KEEPS its workspace and every file already sent, so do " +
		"NOT re-pass `context_msg_ids` for media it already has — pass new " +
		"`context_msg_ids` ONLY if it needs media it has not received yet.\n" +
		"3. FAILED and not worth retrying → send one `reply_message` " +
		"telling the user briefly what failed and suggesting next steps."
}

// truncate shortens text to limit characters, marker included.
func truncate(text string, limit int) string {
	runes := []rune(text)
	if limit <= 0 || len(runes) <= limit {
		return text
	}
	const marker = " …[truncated]"
	keep := max(0, limit-len([]rune(marker)))
	return string(runes[:keep]) + marker
}
