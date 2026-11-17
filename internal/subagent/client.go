package subagent

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
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
	"strconv"
	"strings"
	"time"
)

// maxUploadChunkBytes bounds one upload chunk whatever the service asks for.
const maxUploadChunkBytes = 8 * 1024 * 1024

// maxClientBodyBytes bounds a response body the client reads.
const maxClientBodyBytes = 4 * 1024 * 1024

// RequestError is a failed request to the service. Status is the last HTTP
// status, or zero when the service was not reached.
type RequestError struct {
	Message string
	Status  int
}

func (err *RequestError) Error() string { return err.Message }

func requestErrorf(status int, format string, args ...any) error {
	return &RequestError{Message: fmt.Sprintf(format, args...), Status: status}
}

// Client talks to a WazzapSubAgents service.
type Client struct {
	config Config
	http   *http.Client
	// sleep waits between retries; tests replace it.
	sleep func(context.Context, time.Duration) error
}

func NewClient(config Config) *Client {
	config = config.withDefaults()
	config.URL = strings.TrimRight(strings.TrimSpace(config.URL), "/")
	return &Client{
		config: config,
		http: &http.Client{
			Timeout: config.HTTPTimeout,
			// A redirect could carry the API token to another host.
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
		sleep: sleepContext,
	}
}

func sleepContext(ctx context.Context, duration time.Duration) error {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// fileIdentity is what both sides check a transferred file against.
type fileIdentity struct {
	Name   string
	Size   int64
	SHA256 string
}

type response struct {
	status     int
	body       map[string]any
	retryAfter string
}

// Submit hands a task to the service. It returns once the service accepted
// the task and confirmed every input file by size and checksum. Rate limits,
// server errors and network failures are retried.
func (client *Client) Submit(ctx context.Context, sessionID, instruction string, files []string, highQuality bool, previousSessionID string) error {
	manifest, err := fileManifest(files)
	if err != nil {
		return err
	}
	wireFiles, inlineFiles, err := client.prepareTransfer(ctx, files, manifest)
	if err != nil {
		return err
	}
	// The webhook tells callbacks apart by the body's "type", so completion
	// and progress share one URL.
	payload := map[string]any{
		"session_id":          sessionID,
		"instruction":         instruction,
		"input_files":         wireFiles,
		"input_files_content": inlineFiles,
		"callback_url":        client.config.WebhookURL,
		"progress_webhook":    client.config.WebhookURL,
		"high_quality":        highQuality,
	}
	if previousSessionID != "" {
		payload["previous_session_id"] = previousSessionID
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return requestErrorf(0, "encode sub-agent request: %v", err)
	}
	attempts := client.config.SubmitRetryMax + 1
	for attempt := 1; ; attempt++ {
		reply, err := client.do(ctx, http.MethodPost, client.config.URL+"/execute", body, map[string]string{"Content-Type": "application/json"})
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if attempt >= attempts {
				return requestErrorf(0, "Failed to reach SubAgent after %d attempts: %v", attempts, err)
			}
			if err := client.sleep(ctx, client.backoff(attempt, "")); err != nil {
				return err
			}
			continue
		}
		if reply.status >= 200 && reply.status < 300 {
			ackErr := validateStagingAck(reply, manifest, "/execute", sessionID)
			if ackErr == nil {
				return nil
			}
			// A replayed request may be answered before the first one staged
			// its files.
			if reply.body["idempotent_replay"] == true && attempt < attempts {
				if err := client.sleep(ctx, client.backoff(attempt, reply.retryAfter)); err != nil {
					return err
				}
				continue
			}
			return ackErr
		}
		if !retryableStatus(reply.status) || attempt >= attempts {
			return remoteError("/execute", reply)
		}
		if err := client.sleep(ctx, client.backoff(attempt, reply.retryAfter)); err != nil {
			return err
		}
	}
}

// Steer sends instruction, and files, to the running task sessionID. It
// returns once the service staged every file and the task consumed the
// instruction.
func (client *Client) Steer(ctx context.Context, sessionID, instruction string, files []string) error {
	steeringID, err := randomHex(16)
	if err != nil {
		return requestErrorf(0, "create steering ID: %v", err)
	}
	manifest, err := fileManifest(files)
	if err != nil {
		return err
	}
	payload := map[string]any{"session_id": sessionID, "steering_id": steeringID, "instruction": instruction}
	if len(files) > 0 {
		wireFiles, inlineFiles, err := client.prepareTransfer(ctx, files, manifest)
		if err != nil {
			return err
		}
		payload["input_files"], payload["input_files_content"] = wireFiles, inlineFiles
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return requestErrorf(0, "encode sub-agent steering: %v", err)
	}
	attempts := client.config.SubmitRetryMax + 1
	for attempt := 1; ; attempt++ {
		reply, err := client.do(ctx, http.MethodPost, client.config.URL+"/steer", body, map[string]string{"Content-Type": "application/json"})
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if attempt >= attempts {
				return requestErrorf(0, "Failed to reach SubAgent steering endpoint after %d attempts: %v", attempts, err)
			}
			if err := client.sleep(ctx, client.backoff(attempt, "")); err != nil {
				return err
			}
			continue
		}
		if reply.status >= 200 && reply.status < 300 {
			if err := validateStagingAck(reply, manifest, "/steer", sessionID); err != nil {
				return err
			}
			if text(reply.body["steering_id"]) != steeringID {
				return requestErrorf(reply.status, "SubAgent /steer returned a mismatched steering_id")
			}
			return client.waitSteeringConsumed(ctx, sessionID, steeringID)
		}
		if !retryableStatus(reply.status) || attempt >= attempts {
			return remoteError("/steer", reply)
		}
		if err := client.sleep(ctx, client.backoff(attempt, reply.retryAfter)); err != nil {
			return err
		}
	}
}

func (client *Client) waitSteeringConsumed(ctx context.Context, sessionID, steeringID string) error {
	target := client.config.URL + "/sessions/" + url.PathEscape(sessionID) + "/steering/" + url.PathEscape(steeringID)
	deadline := time.Now().Add(client.config.SteerConsumeTimeout)
	for time.Now().Before(deadline) {
		reply, err := client.do(ctx, http.MethodGet, target, nil, nil)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		state := ""
		if err == nil {
			state = text(reply.body["state"])
			if reply.status == http.StatusOK && state == "consumed" {
				return nil
			}
		}
		// A network failure, a server error and a queued instruction are all
		// worth another look.
		if err != nil || reply.status >= 500 || (reply.status == http.StatusOK && state == "queued") {
			if err := client.sleep(ctx, 250*time.Millisecond); err != nil {
				return err
			}
			continue
		}
		return requestErrorf(reply.status, "SubAgent steering status failed status=%d state=%s", reply.status, state)
	}
	return requestErrorf(0, "SubAgent accepted steering but did not consume it before the timeout")
}

// prepareTransfer decides how each file travels: small ones inline in the
// request (base64), the rest uploaded first and named by upload ID. A path
// alone is never relied on: it only works when both sides share a disk.
func (client *Client) prepareTransfer(ctx context.Context, files []string, manifest []fileIdentity) (wire []any, inline []map[string]any, err error) {
	wire, inline = make([]any, 0, len(files)), make([]map[string]any, 0, len(files))
	var inlineTotal int64
	for index, path := range files {
		identity := manifest[index]
		if identity.Size <= client.config.MaxInlineFileBytes && inlineTotal+identity.Size <= client.config.MaxInlineTotalBytes {
			data, readErr := os.ReadFile(path)
			if readErr != nil {
				return nil, nil, requestErrorf(0, "failed reading input file %s: %v", identity.Name, readErr)
			}
			digest := sha256.Sum256(data)
			if int64(len(data)) != identity.Size || hex.EncodeToString(digest[:]) != identity.SHA256 {
				return nil, nil, requestErrorf(0, "input file changed during transfer: %s", identity.Name)
			}
			inlineTotal += identity.Size
			inline = append(inline, map[string]any{
				"name": identity.Name, "content_base64": base64.StdEncoding.EncodeToString(data),
				"size": identity.Size, "sha256": identity.SHA256,
			})
			// The path stays beside the inline bytes for services that share
			// this disk; the receiver prefers the verified bytes.
			wire = append(wire, path)
			continue
		}
		uploaded, uploadErr := client.upload(ctx, path, identity)
		if uploadErr != nil {
			return nil, nil, uploadErr
		}
		wire = append(wire, uploaded)
	}
	return wire, inline, nil
}

// upload sends one file in chunks. Each chunk and the whole file are
// confirmed by checksum.
func (client *Client) upload(ctx context.Context, path string, identity fileIdentity) (map[string]any, error) {
	uploadID, err := randomHex(16)
	if err != nil {
		return nil, requestErrorf(0, "create upload ID: %v", err)
	}
	initBody, err := json.Marshal(map[string]any{"upload_id": uploadID, "filename": identity.Name, "size": identity.Size, "sha256": identity.SHA256})
	if err != nil {
		return nil, requestErrorf(0, "encode upload: %v", err)
	}
	opened, err := client.uploadRequest(ctx, http.MethodPost, client.config.URL+"/uploads/init", initBody, map[string]string{"Content-Type": "application/json"})
	if err != nil {
		return nil, err
	}
	if err := validateUploadIdentity(opened, uploadID, identity, false); err != nil {
		return nil, err
	}
	chunkSize, ok := integer(opened.body["max_chunk_bytes"])
	if !ok || chunkSize <= 0 {
		return nil, requestErrorf(opened.status, "SubAgent upload init returned invalid max_chunk_bytes")
	}
	chunkSize = min(chunkSize, maxUploadChunkBytes)
	file, err := os.Open(path)
	if err != nil {
		return nil, requestErrorf(0, "failed reading input file %s: %v", identity.Name, err)
	}
	defer file.Close()
	buffer := make([]byte, chunkSize)
	var offset int64
	for index := 0; offset < identity.Size; index++ {
		read, readErr := io.ReadFull(file, buffer[:min(chunkSize, identity.Size-offset)])
		if readErr != nil || read == 0 {
			return nil, requestErrorf(0, "input file changed/truncated during upload: %s", identity.Name)
		}
		data := buffer[:read]
		end := offset + int64(read) - 1
		digest := sha256.Sum256(data)
		reply, err := client.uploadRequest(ctx, http.MethodPut, fmt.Sprintf("%s/uploads/%s/chunks/%d", client.config.URL, uploadID, index), data, map[string]string{
			"Content-Range": fmt.Sprintf("bytes %d-%d/%d", offset, end, identity.Size),
			"Content-Type":  "application/octet-stream",
		})
		if err != nil {
			return nil, err
		}
		chunk, _ := reply.body["chunk"].(map[string]any)
		chunkIndex, _ := integer(chunk["index"])
		chunkStart, _ := integer(chunk["start"])
		chunkEnd, _ := integer(chunk["end"])
		chunkLength, _ := integer(chunk["size"])
		if chunk == nil || chunkIndex != int64(index) || chunkStart != offset || chunkEnd != end ||
			chunkLength != int64(read) || text(chunk["sha256"]) != hex.EncodeToString(digest[:]) {
			return nil, requestErrorf(reply.status, "SubAgent upload returned a mismatched chunk receipt")
		}
		offset = end + 1
	}
	if extra, _ := file.Read(make([]byte, 1)); extra > 0 {
		return nil, requestErrorf(0, "input file grew during upload: %s", identity.Name)
	}
	completed, err := client.uploadRequest(ctx, http.MethodPost, client.config.URL+"/uploads/"+uploadID+"/complete", nil, nil)
	if err != nil {
		return nil, err
	}
	if err := validateUploadIdentity(completed, uploadID, identity, true); err != nil {
		return nil, err
	}
	return map[string]any{"upload_id": uploadID, "name": identity.Name, "size": identity.Size, "sha256": identity.SHA256}, nil
}

// uploadRequest sends one upload call, retrying rate limits, server errors
// and network failures. Chunks are idempotent, so a retry is safe.
func (client *Client) uploadRequest(ctx context.Context, method, target string, body []byte, headers map[string]string) (response, error) {
	attempts := client.config.SubmitRetryMax + 1
	for attempt := 1; ; attempt++ {
		reply, err := client.do(ctx, method, target, body, headers)
		if ctx.Err() != nil {
			return response{}, ctx.Err()
		}
		if err == nil && reply.status >= 200 && reply.status < 300 && reply.body["success"] == true {
			return reply, nil
		}
		if err == nil && (!retryableStatus(reply.status) || attempt >= attempts) {
			return response{}, requestErrorf(reply.status, "SubAgent upload returned status=%d", reply.status)
		}
		if err != nil && attempt >= attempts {
			return response{}, requestErrorf(0, "SubAgent upload request failed after %d attempts: %v", attempts, err)
		}
		if err := client.sleep(ctx, client.backoff(attempt, reply.retryAfter)); err != nil {
			return response{}, err
		}
	}
}

func validateUploadIdentity(reply response, uploadID string, expected fileIdentity, complete bool) error {
	size, _ := integer(reply.body["size"])
	if text(reply.body["upload_id"]) != uploadID || text(reply.body["filename"]) != expected.Name ||
		size != expected.Size || text(reply.body["sha256"]) != expected.SHA256 ||
		(complete && (reply.body["complete"] != true || text(reply.body["state"]) != "complete")) {
		return requestErrorf(reply.status, "SubAgent upload returned a mismatched identity receipt")
	}
	return nil
}

// validateStagingAck requires the service to have accepted the request and
// staged exactly the files sent, matched by size and checksum.
func validateStagingAck(reply response, expected []fileIdentity, operation, sessionID string) error {
	body := reply.body
	if body["accepted"] != true {
		return requestErrorf(reply.status, "SubAgent %s did not explicitly accept the request", operation)
	}
	if text(body["session_id"]) != sessionID {
		return requestErrorf(reply.status, "SubAgent %s returned a mismatched session_id", operation)
	}
	if operation == "/execute" {
		if status := text(body["status"]); (status != "processing" && status != "completed") || text(body["request_fingerprint"]) == "" {
			return requestErrorf(reply.status, "SubAgent /execute returned an incomplete acceptance receipt")
		}
	} else if state := text(body["state"]); state != "queued" && state != "consumed" {
		return requestErrorf(reply.status, "SubAgent /steer did not confirm that steering was queued")
	}
	requested, _ := integer(body["requested_file_count"])
	stagedCount, _ := integer(body["staged_file_count"])
	staged, _ := body["staged_files"].([]any)
	fileErrors, _ := body["file_errors"].([]any)
	if requested != int64(len(expected)) || stagedCount != int64(len(expected)) || len(staged) != len(expected) || len(fileErrors) != 0 {
		return requestErrorf(reply.status, "SubAgent %s staged only part of the requested input", operation)
	}
	type fileKey struct {
		sha  string
		size int64
	}
	remaining := make(map[fileKey]int, len(expected))
	for _, file := range expected {
		remaining[fileKey{file.SHA256, file.Size}]++
	}
	for _, raw := range staged {
		item, ok := raw.(map[string]any)
		if !ok {
			return requestErrorf(reply.status, "SubAgent %s returned an invalid staged file manifest", operation)
		}
		size, _ := integer(item["size"])
		key := fileKey{text(item["sha256"]), size}
		if remaining[key] == 0 {
			return requestErrorf(reply.status, "SubAgent %s staged file checksum/size mismatch", operation)
		}
		remaining[key]--
	}
	return nil
}

// fileManifest reads the size and checksum of every file to send.
func fileManifest(files []string) ([]fileIdentity, error) {
	manifest := make([]fileIdentity, 0, len(files))
	for _, path := range files {
		info, err := os.Stat(path)
		if err != nil || !info.Mode().IsRegular() {
			return nil, requestErrorf(0, "input file is missing or not regular: %s", filepath.Base(path))
		}
		digest, size, err := fileDigest(path)
		if err != nil {
			return nil, requestErrorf(0, "failed reading input file %s: %v", filepath.Base(path), err)
		}
		manifest = append(manifest, fileIdentity{Name: filepath.Base(path), Size: size, SHA256: digest})
	}
	return manifest, nil
}

func fileDigest(path string) (string, int64, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", 0, err
	}
	defer file.Close()
	hash := sha256.New()
	size, err := io.Copy(hash, file)
	if err != nil {
		return "", 0, err
	}
	return hex.EncodeToString(hash.Sum(nil)), size, nil
}

func (client *Client) do(ctx context.Context, method, target string, body []byte, headers map[string]string) (response, error) {
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	request, err := http.NewRequestWithContext(ctx, method, target, reader)
	if err != nil {
		return response{}, err
	}
	for name, value := range headers {
		request.Header.Set(name, value)
	}
	if client.config.APIToken != "" {
		request.Header.Set("Authorization", "Bearer "+client.config.APIToken)
	}
	request.Header.Set("Accept", "application/json")
	httpResponse, err := client.http.Do(request)
	if err != nil {
		var requestError *url.Error
		if errors.As(err, &requestError) && requestError.Err != nil {
			// Keep the cause without repeating the configured URL.
			err = requestError.Err
		}
		return response{}, err
	}
	defer httpResponse.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(httpResponse.Body, maxClientBodyBytes))
	if err != nil {
		return response{}, err
	}
	reply := response{status: httpResponse.StatusCode, retryAfter: httpResponse.Header.Get("Retry-After")}
	if json.Unmarshal(raw, &reply.body) != nil || reply.body == nil {
		reply.body = map[string]any{}
	}
	return reply, nil
}

// backoff is the wait before retry number attempt. A Retry-After header
// wins over the exponential schedule.
func (client *Client) backoff(attempt int, retryAfter string) time.Duration {
	if seconds, err := strconv.ParseFloat(strings.TrimSpace(retryAfter), 64); err == nil && seconds >= 0 {
		return min(client.config.RetryMaxBackoff, time.Duration(seconds*float64(time.Second)))
	}
	wait := client.config.RetryBaseBackoff
	for step := 1; step < attempt && wait < client.config.RetryMaxBackoff; step++ {
		wait *= 2
	}
	return min(client.config.RetryMaxBackoff, wait)
}

func retryableStatus(status int) bool {
	return status == http.StatusTooManyRequests || (status >= 500 && status < 600)
}

// remoteError describes a refused request without echoing secrets.
func remoteError(operation string, reply response) error {
	if reply.status == http.StatusUnauthorized {
		return requestErrorf(reply.status, "SubAgent %s authentication was rejected (HTTP 401). "+
			"Set the sub-agent API token to the exact same value here and in WazzapSubAgents, then restart both", operation)
	}
	if report := strings.TrimSpace(text(reply.body["report"])); report != "" {
		return requestErrorf(reply.status, "SubAgent %s returned status=%d: %s", operation, reply.status, report)
	}
	return requestErrorf(reply.status, "SubAgent %s returned status=%d", operation, reply.status)
}

func randomHex(bytesCount int) (string, error) {
	raw := make([]byte, bytesCount)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return hex.EncodeToString(raw), nil
}

// text returns value when it is a JSON string.
func text(value any) string {
	result, _ := value.(string)
	return result
}

// integer returns value when it is a whole JSON number.
func integer(value any) (int64, bool) {
	number, ok := value.(float64)
	if !ok || number != float64(int64(number)) {
		return 0, false
	}
	return int64(number), true
}
