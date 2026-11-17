package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/Chomosuke9/WazzapAgent-Go/internal/action"
	appsqlite "github.com/Chomosuke9/WazzapAgent-Go/internal/adapters/sqlite"
	whatsapp "github.com/Chomosuke9/WazzapAgent-Go/internal/adapters/whatsapp/hypermeow"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/agent"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/conversation"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/identity"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/subagent"
)

// subagentMessenger sends what the sub-agent flow writes to a chat (notices,
// raw reports, output files) and records each as the bot's own message, so
// the model sees it and a later task can take a sent file as input.
type subagentMessenger struct {
	adapter *whatsapp.Adapter
	store   *appsqlite.SubagentStore
	// authorize is the send policy every bot message passes.
	authorize func(context.Context, agent.Key) error
	clock     agent.Clock
}

func (messenger subagentMessenger) SendText(ctx context.Context, key agent.Key, text string, quoted identity.MessageID) error {
	if err := messenger.authorize(ctx, key); err != nil {
		return err
	}
	actionID, err := identity.NewActionID()
	if err != nil {
		return agent.NewError(agent.ErrorInternal, "send sub-agent message", err)
	}
	sent, err := messenger.adapter.SendText(ctx, action.SendTextRequest{Key: key, ActionID: actionID, Text: text, QuotedMessageID: quoted})
	if err != nil {
		return err
	}
	// The message is out: a failed record only hides it from the transcript.
	_ = messenger.store.RecordSubagentMessage(context.WithoutCancel(ctx), key, text, sent.ProviderReceipt, nil, messenger.clock.Now())
	return nil
}

func (messenger subagentMessenger) SendFile(ctx context.Context, key agent.Key, file subagent.StagedFile) error {
	if err := messenger.authorize(ctx, key); err != nil {
		return err
	}
	receipt, err := messenger.adapter.SendFile(ctx, key, whatsapp.OutboundFile{Path: file.Path, Name: file.Name, Mime: file.Mime, Kind: file.Kind})
	if err != nil {
		return err
	}
	// The transcript shows the file like an incoming one, so the model can
	// name it in <files_in_chat>.
	placeholder := map[string]string{
		"image": conversation.PlaceholderImage, "video": conversation.PlaceholderVideo, "audio": conversation.PlaceholderAudio,
	}[file.Kind]
	if placeholder == "" {
		placeholder = conversation.PlaceholderDocument
	}
	sent := &subagent.SentFile{Path: file.Path, Name: file.Name, Mime: file.Mime, Kind: file.Kind, Size: file.Size}
	_ = messenger.store.RecordSubagentMessage(context.WithoutCancel(ctx), key, placeholder+" "+file.Name, receipt, sent, messenger.clock.Now())
	return nil
}

// subagentInputs reads a chat message for a task: the file of a message
// someone sent (downloaded from WhatsApp), or a file the bot sent for an
// earlier task, plus the text its sender typed.
type subagentInputs struct {
	adapter *whatsapp.Adapter
	inbound *appsqlite.InboundStore
	store   *appsqlite.SubagentStore
}

func (inputs subagentInputs) MessageInput(ctx context.Context, key agent.Key, messageID identity.MessageID, dir string) (subagent.MessageInput, error) {
	text, fromBot, err := inputs.store.MessageText(ctx, key, messageID)
	if err != nil {
		return subagent.MessageInput{}, err
	}
	_, _, isFile := agent.FileKind(text)
	if fromBot {
		file, err := inputs.store.SentFile(ctx, key, messageID)
		switch {
		case err == nil:
			path, copyErr := copyInto(dir, file.Path, file.Name)
			if copyErr != nil {
				return subagent.MessageInput{}, agent.NewError(agent.ErrorNotFound, "read sub-agent input", fmt.Errorf("the file the bot sent is no longer available: %w", copyErr))
			}
			return subagent.MessageInput{Paths: []string{path}}, nil
		case !agent.IsCode(err, agent.ErrorNotFound):
			return subagent.MessageInput{}, err
		case isFile:
			return subagent.MessageInput{}, agent.NewError(agent.ErrorNotFound, "read sub-agent input", errors.New("the file the bot sent is no longer available"))
		}
		return subagent.MessageInput{Text: text}, nil
	}
	var input subagent.MessageInput
	payload, err := inputs.inbound.ReadMessageMedia(ctx, key, messageID)
	switch {
	case err == nil:
		path, downloadErr := inputs.adapter.DownloadFile(ctx, payload, dir)
		if downloadErr != nil {
			return subagent.MessageInput{}, downloadErr
		}
		input.Paths = []string{path}
	case !agent.IsCode(err, agent.ErrorNotFound):
		return subagent.MessageInput{}, err
	case isFile:
		// A file message whose media was not kept (view-once, or older than
		// this feature): its caption alone is not what the model asked for.
		return subagent.MessageInput{}, agent.NewError(agent.ErrorNotFound, "read sub-agent input", errors.New("the message's file is not available"))
	}
	input.Text = conversation.AuthoredText(text)
	return input, nil
}

// copyInto copies source to dir/name, adding _1, _2, ... when name is taken.
func copyInto(dir, source, name string) (string, error) {
	in, err := os.Open(source)
	if err != nil {
		return "", err
	}
	defer in.Close()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	extension := filepath.Ext(name)
	for counter := 0; counter < 1000; counter++ {
		candidate := name
		if counter > 0 {
			candidate = fmt.Sprintf("%s_%d%s", name[:len(name)-len(extension)], counter, extension)
		}
		path := filepath.Join(dir, candidate)
		out, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if errors.Is(err, os.ErrExist) {
			continue
		}
		if err != nil {
			return "", err
		}
		_, copyErr := io.Copy(out, in)
		if err := errors.Join(copyErr, out.Close()); err != nil {
			_ = os.Remove(path)
			return "", err
		}
		return path, nil
	}
	return "", errors.New("no free file name")
}

// subagentRuntime is the sub-agent of a running Agent: the coordinator and
// the callback server it listens on.
type subagentRuntime struct {
	coordinator *subagent.Coordinator
	listener    net.Listener
}

// openSubagent creates the sub-agent when a service URL is configured. A
// webhook address that cannot be bound is logged and leaves the Agent running
// without a sub-agent: a task the service could never report back on would
// only leave the chat waiting.
func (application *Application) openSubagent(store *appsqlite.Store, adapter *whatsapp.Adapter, authorize func(context.Context, agent.Key) error) *subagentRuntime {
	settings := application.config.Subagent()
	if settings.URL == "" {
		return nil
	}
	if !settings.WebhookLoopback() && settings.WebhookToken == "" {
		application.logger.Error("sub-agent disabled: a webhook token is required when the webhook listens beyond loopback", "address", settings.WebhookAddress)
		return nil
	}
	listener, err := net.Listen("tcp", settings.WebhookAddress)
	if err != nil {
		application.logger.Error("sub-agent disabled: cannot listen for callbacks", "address", settings.WebhookAddress, "error", err)
		return nil
	}
	coordinator, err := subagent.NewCoordinator(subagent.Config{
		URL: settings.URL, APIToken: settings.APIToken, WebhookURL: settings.WebhookURL, WebhookToken: settings.WebhookToken,
		DataDir: filepath.Join(application.config.TenantDataDir(), "subagent"), EnabledDefault: settings.EnabledDefault,
		WaitTimeout: settings.WaitTimeout, MaxWait: settings.MaxWait,
	}, subagent.Dependencies{
		TenantID: application.config.TenantID(), AccountID: application.config.AccountID(), Store: store.Subagent(),
		Messenger: subagentMessenger{adapter: adapter, store: store.Subagent(), authorize: authorize, clock: agent.SystemClock{}},
		Inputs:    subagentInputs{adapter: adapter, inbound: store.Inbound(), store: store.Subagent()},
		Authorize: authorize, Logger: application.logger.With("component", "subagent"),
	})
	if err != nil {
		_ = listener.Close()
		application.logger.Error("sub-agent disabled", "error", err)
		return nil
	}
	return &subagentRuntime{coordinator: coordinator, listener: listener}
}

// serve answers the service's callbacks until ctx ends, then stops waiting
// for tasks. Tasks stay saved for the next start.
func (runtime *subagentRuntime) serve(ctx context.Context, logger interface {
	Info(string, ...any)
	Error(string, ...any)
}) error {
	server := &http.Server{
		Handler: runtime.coordinator.Handler(), ReadHeaderTimeout: 10 * time.Second,
		// A completion may carry large inline files.
		ReadTimeout: 10 * time.Minute, WriteTimeout: 20 * time.Minute, IdleTimeout: time.Minute,
	}
	done := make(chan error, 1)
	go func() { done <- server.Serve(runtime.listener) }()
	logger.Info("sub-agent webhook started", "address", runtime.listener.Addr().String())
	select {
	case <-ctx.Done():
	case err := <-done:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("sub-agent webhook stopped", "error", err)
		}
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := server.Shutdown(shutdownCtx); err != nil {
		_ = server.Close()
	}
	runtime.coordinator.Close()
	return nil
}
