package subagent

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/Chomosuke9/WazzapAgent-Go/internal/agent"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/identity"
)

// errOutputDownload is a failure fetching an output file: the service is
// asked to send the callback again. errOutputInvalid is a callback whose
// output cannot be trusted: sending it again would not help.
var (
	errOutputDownload = errors.New("output download failed")
	errOutputInvalid  = errors.New("output is invalid")
)

var sha256Pattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

// sessionPattern is "<chat ID>_<8 hex>_<unix time>", the form newTask writes.
var sessionPattern = regexp.MustCompile(`^(.+)_([0-9a-f]{8})_([0-9]+)$`)

// Handler serves the callbacks of the WazzapSubAgents service:
//
//	POST /subagent/callback   progress, completion and queue events
//	GET  /health              liveness, probed by the service before it calls back
//
// A completion is answered 2xx only once its result and files are saved, so
// the service keeps its copy until then.
func (coordinator *Coordinator) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/subagent/callback", coordinator.handleCallback)
	mux.HandleFunc("/health", func(writer http.ResponseWriter, _ *http.Request) {
		writeJSON(writer, http.StatusOK, map[string]any{"status": "ok"})
	})
	return mux
}

func (coordinator *Coordinator) authorized(request *http.Request) bool {
	expected := coordinator.config.WebhookToken
	if expected == "" {
		return true
	}
	provided := request.Header.Get("X-Subagent-Webhook-Token")
	if provided == "" {
		if scheme, token, found := strings.Cut(request.Header.Get("Authorization"), " "); found && strings.EqualFold(scheme, "Bearer") {
			provided = strings.TrimSpace(token)
		}
	}
	return provided != "" && subtle.ConstantTimeCompare([]byte(provided), []byte(expected)) == 1
}

func (coordinator *Coordinator) handleCallback(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodPost {
		writeJSON(writer, http.StatusMethodNotAllowed, map[string]any{"status": "method_not_allowed"})
		return
	}
	if !coordinator.authorized(request) {
		coordinator.logger.Warn("sub-agent callback rejected: invalid token")
		writeJSON(writer, http.StatusUnauthorized, map[string]any{"status": "unauthorized"})
		return
	}
	request.Body = http.MaxBytesReader(writer, request.Body, coordinator.config.WebhookMaxBodyBytes)
	var data map[string]any
	if err := json.NewDecoder(request.Body).Decode(&data); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			coordinator.logger.Warn("sub-agent callback rejected: body too large", "max_bytes", coordinator.config.WebhookMaxBodyBytes)
			http.Error(writer, "Request body too large", http.StatusRequestEntityTooLarge)
			return
		}
		http.Error(writer, "Invalid JSON", http.StatusBadRequest)
		return
	}
	sessionID := strings.TrimSpace(text(data["session_id"]))
	if sessionID == "" || len(sessionID) > 512 {
		http.Error(writer, "Invalid session_id", http.StatusBadRequest)
		return
	}
	switch kind := text(data["type"]); kind {
	case "progress":
		coordinator.handleProgress(writer, sessionID, data)
	case "complete":
		coordinator.handleComplete(writer, request, sessionID, data)
	case "steering":
		// Steering was already confirmed by the /steer response.
		writeJSON(writer, http.StatusOK, map[string]any{"status": "ok"})
	case "queued", "queue_advanced", "queue_status":
		coordinator.handleQueue(writer, request, sessionID, kind, data)
	default:
		http.Error(writer, "Unknown type", http.StatusBadRequest)
	}
}

func (coordinator *Coordinator) handleProgress(writer http.ResponseWriter, sessionID string, data map[string]any) {
	entry, ok := data["entry"].(map[string]any)
	if !ok && data["entry"] != nil {
		http.Error(writer, "Invalid progress entry", http.StatusBadRequest)
		return
	}
	step := scalarText(entry["step"])
	if step == "" {
		step = "unknown"
	}
	limit := coordinator.config.ProgressDetailMaxChars
	progress := ProgressEntry{
		Step: step, Detail: truncate(scalarText(entry["detail"]), limit),
		Reason: truncate(scalarText(entry["reason"]), limit), At: coordinator.clock.Now(),
	}
	coordinator.mu.Lock()
	live := coordinator.live[sessionID]
	if live != nil {
		last := len(live.progress) - 1
		// A callback sent twice adds nothing.
		if last < 0 || live.progress[last].Step != progress.Step || live.progress[last].Detail != progress.Detail || live.progress[last].Reason != progress.Reason {
			live.progress = append(live.progress, progress)
			if len(live.progress) > maxProgressEntries {
				live.progress = live.progress[len(live.progress)-maxProgressEntries:]
			}
		}
	}
	coordinator.mu.Unlock()
	coordinator.signalActivity(live)
	coordinator.logger.Info("sub-agent progress", "session", sessionID, "step", step, "reason", clip(progress.Reason, 160), "detail", clip(progress.Detail, 160))
	writeJSON(writer, http.StatusOK, map[string]any{"status": "ok"})
}

func (coordinator *Coordinator) signalActivity(live *liveTask) {
	if live == nil {
		return
	}
	select {
	case live.activity <- struct{}{}:
	default:
	}
}

func (coordinator *Coordinator) handleComplete(writer http.ResponseWriter, request *http.Request, sessionID string, data map[string]any) {
	raw, ok := data["result"].(map[string]any)
	if !ok && data["result"] != nil {
		http.Error(writer, "Invalid result", http.StatusBadRequest)
		return
	}
	ctx := request.Context()
	task, err := coordinator.store.LoadTask(ctx, coordinator.tenantID, sessionID)
	if agent.IsCode(err, agent.ErrorNotFound) {
		// The task is unknown here, for example after the database was
		// replaced. Its chat is in the session ID: take the result if that
		// chat exists, else ask the service to keep it.
		task, err = coordinator.adoptSession(ctx, sessionID)
		if agent.IsCode(err, agent.ErrorNotFound) {
			coordinator.logger.Warn("sub-agent completion for an unknown session", "session", sessionID)
			writeJSON(writer, http.StatusConflict, map[string]any{"status": "pending_registration", "retryable": true})
			return
		}
	}
	if err != nil {
		coordinator.logger.Error("sub-agent completion could not be read", "session", sessionID, "error", err)
		writeJSON(writer, http.StatusServiceUnavailable, map[string]any{"status": "persistence_unavailable", "retryable": true})
		return
	}
	if task.Status != StatusRunning {
		// Already finished here (a repeated callback, or one that came after
		// the task was given up): there is nothing more to keep.
		writeJSON(writer, http.StatusOK, map[string]any{"status": "ok", "duplicate": true})
		return
	}
	result, report, err := coordinator.materializeResult(ctx, sessionID, raw)
	switch {
	case errors.Is(err, errOutputInvalid):
		coordinator.logger.Warn("sub-agent callback output rejected", "session", sessionID, "error", err)
		writeJSON(writer, http.StatusUnprocessableEntity, map[string]any{"status": "invalid_output", "retryable": false})
		return
	case err != nil:
		coordinator.logger.Warn("sub-agent output download failed", "session", sessionID, "error", err)
		writeJSON(writer, http.StatusBadGateway, map[string]any{"status": "output_download_failed", "retryable": true})
		return
	}
	if _, err := coordinator.finish(sessionID, result, report); err != nil {
		writeJSON(writer, http.StatusServiceUnavailable, map[string]any{"status": "persistence_unavailable", "retryable": true})
		return
	}
	coordinator.logger.Info("sub-agent task complete", "session", sessionID, "success", result.Success, "files", len(result.Files))
	// A task with a waiter is delivered by it. One without (the app was
	// restarted and has not recovered it yet) is delivered from here.
	if coordinator.claim(sessionID) && !coordinator.spawn(func() {
		defer coordinator.release(sessionID)
		coordinator.deliver(sessionID)
	}) {
		coordinator.release(sessionID)
	}
	writeJSON(writer, http.StatusOK, map[string]any{"status": "ok", "delivery_pending": false})
}

// adoptSession registers a finished task this app has no record of, so its
// result can still reach its chat. It fails with agent.ErrorNotFound when the
// session ID does not name one of this account's chats.
func (coordinator *Coordinator) adoptSession(ctx context.Context, sessionID string) (Task, error) {
	unknown := agent.NewError(agent.ErrorNotFound, "adopt sub-agent session", errors.New("session does not belong to a known chat"))
	match := sessionPattern.FindStringSubmatch(sessionID)
	if match == nil {
		return Task{}, unknown
	}
	chatID, err := identity.ParseChatID(match[1])
	if err != nil {
		return Task{}, unknown
	}
	key := agent.Key{TenantID: coordinator.tenantID, AccountID: coordinator.accountID, ChatID: chatID}
	exists, err := coordinator.store.ChatExists(ctx, key)
	if err != nil {
		return Task{}, err
	}
	if !exists {
		return Task{}, unknown
	}
	if active, err := coordinator.store.ActiveTask(ctx, key); err != nil || active != nil {
		// The chat is busy with another task; the service will call again.
		return Task{}, errors.Join(err, unknown)
	}
	invocationID, invocationErr := identity.NewInvocationID()
	causationID, causationErr := identity.NewCausationID()
	if err := errors.Join(invocationErr, causationErr); err != nil {
		return Task{}, agent.NewError(agent.ErrorInternal, "adopt sub-agent session", err)
	}
	task := Task{
		Key: key, SessionID: sessionID, Instruction: "Recovered durable completion callback",
		InvocationID: invocationID, CausationID: causationID, Status: StatusRunning, Submitted: true,
		StartedAt: coordinator.clock.Now(),
	}
	if err := coordinator.store.CreateTask(ctx, task); err != nil {
		return Task{}, err
	}
	return task, nil
}

// materializeResult turns a completion's result into what is kept: whether
// the task succeeded, its report, and its output files written to disk. A
// small file comes inside the callback (base64); a larger one is fetched
// from the service. Every file is checked against its declared size and
// checksum. Paths in the callback are never used: they belong to another
// machine.
func (coordinator *Coordinator) materializeResult(ctx context.Context, sessionID string, raw map[string]any) (Result, string, error) {
	result := Result{Success: raw["success"] == true, ContentDropped: raw["output_files_content_dropped"] == true}
	report := scalarText(raw["report"])
	if report == "" {
		report = scalarText(raw["error"])
	}
	inline, inlineOK := raw["output_files_content"].([]any)
	omitted, omittedOK := raw["output_files_omitted"].([]any)
	if (!inlineOK && raw["output_files_content"] != nil) || (!omittedOK && raw["output_files_omitted"] != nil) {
		return Result{}, "", fmt.Errorf("%w: output manifests must be arrays", errOutputInvalid)
	}
	manifest := append(append([]any(nil), inline...), omitted...)
	if len(manifest) == 0 {
		return result, report, nil
	}
	if len(manifest) > maxOutputFiles {
		return Result{}, "", fmt.Errorf("%w: callback declares more than %d output files", errOutputInvalid, maxOutputFiles)
	}
	dir := coordinator.outputDir(sessionID)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return Result{}, "", fmt.Errorf("%w: %v", errOutputDownload, err)
	}
	var inlineTotal int64
	for index, item := range manifest {
		entry, ok := item.(map[string]any)
		if !ok {
			return Result{}, "", fmt.Errorf("%w: output entry must be an object", errOutputInvalid)
		}
		if entry["local_path"] != nil {
			return Result{}, "", fmt.Errorf("%w: callback-supplied local_path is forbidden", errOutputInvalid)
		}
		file := OutputFile{Name: safeFileName(text(entry["name"]), fmt.Sprintf("output-%d", index)), Mime: text(entry["mime"])}
		size, sizeOK := integer(entry["size"])
		if !sizeOK {
			size, sizeOK = integer(entry["size_bytes"])
		}
		file.Size, file.SHA256 = size, strings.ToLower(strings.TrimSpace(text(entry["sha256"])))
		encoded, downloadURL := text(entry["content_base64"]), strings.TrimSpace(text(entry["download_url"]))
		switch {
		case encoded != "":
			if !sizeOK || size < 0 || !sha256Pattern.MatchString(file.SHA256) {
				return Result{}, "", fmt.Errorf("%w: inline output has invalid size or checksum", errOutputInvalid)
			}
			if size > MaxFileBytes {
				return Result{}, "", fmt.Errorf("%w: inline output exceeds the 200 MB file limit", errOutputInvalid)
			}
			if inlineTotal += size; inlineTotal > maxInlineOutputBytes {
				return Result{}, "", fmt.Errorf("%w: inline outputs exceed the 256 MB aggregate limit", errOutputInvalid)
			}
			data, err := base64.StdEncoding.DecodeString(encoded)
			if err != nil {
				return Result{}, "", fmt.Errorf("%w: inline output is not base64", errOutputInvalid)
			}
			digest := sha256.Sum256(data)
			if int64(len(data)) != size || hex.EncodeToString(digest[:]) != file.SHA256 {
				return Result{}, "", fmt.Errorf("%w: inline output size or checksum mismatch", errOutputInvalid)
			}
			file.Path = filepath.Join(dir, fmt.Sprintf("%04d-%s-%s", index, file.SHA256[:12], file.Name))
			if !fileMatches(file.Path, size, file.SHA256) {
				if err := writeFileAtomic(file.Path, data); err != nil {
					return Result{}, "", fmt.Errorf("%w: %v", errOutputDownload, err)
				}
			}
		case downloadURL != "":
			if !sizeOK || size < 0 {
				return Result{}, "", fmt.Errorf("%w: download manifest has invalid size_bytes", errOutputInvalid)
			}
			if size > MaxFileBytes {
				// A policy outcome, not a transport failure: keep the result and
				// name the file as skipped.
				file.SkipReason = fmt.Sprintf("file too large (%d bytes > 200 MB)", size)
				break
			}
			if !sha256Pattern.MatchString(file.SHA256) {
				return Result{}, "", fmt.Errorf("%w: download manifest has invalid sha256", errOutputInvalid)
			}
			fileID := text(entry["file_id"])
			if fileID == "" {
				fileID = fmt.Sprintf("output-%d", index)
			}
			file.Path = filepath.Join(dir, pathKey(fileID)[:12]+"-"+file.Name)
			if !fileMatches(file.Path, size, file.SHA256) {
				if err := coordinator.downloadOutput(ctx, downloadURL, file.Path, size, file.SHA256); err != nil {
					return Result{}, "", err
				}
			}
		default:
			file.SkipReason = clip(strings.TrimSpace(text(entry["bridge_skip_reason"])), 500)
			if file.SkipReason == "" {
				file.SkipReason = "empty content"
			}
		}
		result.Files = append(result.Files, file)
	}
	return result, report, nil
}

// downloadOutput streams one output file from the service into destination
// and checks its size and checksum. Only the service's own origin is fetched,
// with the API token, and redirects are not followed.
func (coordinator *Coordinator) downloadOutput(ctx context.Context, rawURL, destination string, size int64, checksum string) error {
	base, baseErr := url.Parse(coordinator.config.URL + "/")
	reference, referenceErr := url.Parse(rawURL)
	if baseErr != nil || referenceErr != nil {
		return fmt.Errorf("%w: download_url is invalid", errOutputInvalid)
	}
	target := base.ResolveReference(reference)
	if target.Scheme != base.Scheme || !strings.EqualFold(target.Host, base.Host) {
		return fmt.Errorf("%w: download_url origin does not match the sub-agent URL", errOutputInvalid)
	}
	downloadCtx, cancel := context.WithTimeout(ctx, coordinator.config.OutputDownloadTimeout)
	defer cancel()
	request, err := http.NewRequestWithContext(downloadCtx, http.MethodGet, target.String(), nil)
	if err != nil {
		return fmt.Errorf("%w: %v", errOutputDownload, err)
	}
	if coordinator.config.APIToken != "" {
		request.Header.Set("Authorization", "Bearer "+coordinator.config.APIToken)
	}
	downloader := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	response, err := downloader.Do(request)
	if err != nil {
		return fmt.Errorf("%w: %v", errOutputDownload, err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("%w: output download returned HTTP %d", errOutputDownload, response.StatusCode)
	}
	if header := strings.ToLower(strings.TrimSpace(response.Header.Get("X-Content-SHA256"))); header != "" && header != checksum {
		return fmt.Errorf("%w: output response sha256 header mismatch", errOutputDownload)
	}
	temporary := destination + ".download"
	file, err := os.OpenFile(temporary, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return fmt.Errorf("%w: %v", errOutputDownload, err)
	}
	defer os.Remove(temporary)
	hash := sha256.New()
	// One byte past the declared size is enough to see that the body is too long.
	received, copyErr := io.Copy(io.MultiWriter(file, hash), io.LimitReader(response.Body, size+1))
	closeErr := file.Close()
	switch {
	case copyErr != nil || closeErr != nil:
		return fmt.Errorf("%w: %v", errOutputDownload, errors.Join(copyErr, closeErr))
	case received != size:
		return fmt.Errorf("%w: output download size mismatch (%d != %d)", errOutputDownload, received, size)
	case hex.EncodeToString(hash.Sum(nil)) != checksum:
		return fmt.Errorf("%w: output download sha256 mismatch", errOutputDownload)
	}
	if err := os.Rename(temporary, destination); err != nil {
		return fmt.Errorf("%w: %v", errOutputDownload, err)
	}
	return nil
}

// handleQueue forwards the task's place in the service's queue to its chat.
// Waiting in the queue counts as activity, so a queued task is not given up
// as silent.
func (coordinator *Coordinator) handleQueue(writer http.ResponseWriter, request *http.Request, sessionID, kind string, data map[string]any) {
	position, positionOK := integer(data["position"])
	size, sizeOK := integer(data["queue_size"])
	if (!positionOK && data["position"] != nil) || (!sizeOK && data["queue_size"] != nil) {
		http.Error(writer, "Bad position/queue_size", http.StatusBadRequest)
		return
	}
	coordinator.mu.Lock()
	live := coordinator.live[sessionID]
	last, sent := coordinator.queueSent[sessionID]
	coordinator.mu.Unlock()
	coordinator.signalActivity(live)
	if sent && last.position == position && last.size == size && time.Since(last.at) < queueDedupWindow {
		writeJSON(writer, http.StatusOK, map[string]any{"status": "deduped"})
		return
	}
	task, err := coordinator.store.LoadTask(request.Context(), coordinator.tenantID, sessionID)
	if err != nil || task.Status != StatusRunning {
		// Possible when the callback races the task's own end.
		writeJSON(writer, http.StatusOK, map[string]any{"status": "no_active_task"})
		return
	}
	notice := fmt.Sprintf("current queue: %d", position)
	if kind == "queued" {
		notice = "container is used by other session.\n" + notice
	}
	if err := coordinator.messenger.SendText(request.Context(), task.Key, notice, identity.MessageID{}); err != nil {
		// Not recorded as sent, so the service's retry is delivered.
		coordinator.logger.Warn("sub-agent queue notice could not be sent", "session", sessionID, "error", err)
		writeJSON(writer, http.StatusInternalServerError, map[string]any{"status": "handler_error"})
		return
	}
	coordinator.mu.Lock()
	coordinator.queueSent[sessionID] = queueNotice{position: position, size: size, at: time.Now()}
	coordinator.mu.Unlock()
	writeJSON(writer, http.StatusOK, map[string]any{"status": "ok"})
}

// pathKey is a fixed-length name for a directory or file made from value,
// which may hold characters a file system refuses.
func pathKey(value string) string {
	digest := sha256.Sum256([]byte(value))
	return hex.EncodeToString(digest[:])[:24]
}

// fileMatches reports whether path already holds the expected bytes, so a
// repeated callback does not write or download them again.
func fileMatches(path string, size int64, checksum string) bool {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() != size {
		return false
	}
	digest, _, err := fileDigest(path)
	return err == nil && digest == checksum
}

func writeFileAtomic(path string, data []byte) error {
	temporary, err := os.CreateTemp(filepath.Dir(path), ".inline-*")
	if err != nil {
		return err
	}
	defer os.Remove(temporary.Name())
	if _, err := temporary.Write(data); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	return os.Rename(temporary.Name(), path)
}

// scalarText renders a JSON string, number or boolean as text.
func scalarText(value any) string {
	switch typed := value.(type) {
	case nil:
		return ""
	case string:
		return typed
	default:
		return fmt.Sprint(typed)
	}
}

func clip(value string, limit int) string {
	if runes := []rune(value); len(runes) > limit {
		return string(runes[:limit])
	}
	return value
}

func writeJSON(writer http.ResponseWriter, status int, body map[string]any) {
	writer.Header().Set("Content-Type", "application/json")
	writer.WriteHeader(status)
	_ = json.NewEncoder(writer).Encode(body)
}
