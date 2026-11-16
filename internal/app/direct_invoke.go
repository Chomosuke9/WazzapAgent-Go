package app

import (
	"context"
	"errors"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/Chomosuke9/WazzapAgent-Go/internal/agent"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/conversation"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/directinvoke"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/inbound"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/policy"
)

// DirectInvoke makes the bot send a message first in the chat at address: the
// prompt runs as a turn of its own, in the background, and a model call that
// fails on a rate limit or timeout is retried for up to ten minutes. It
// returns once the request is accepted. A chat never seen before is created.
func (application *Application) DirectInvoke(ctx context.Context, address, prompt string) error {
	if application == nil || !application.ready.Load() {
		return agent.NewError(agent.ErrorNotReady, "direct invoke", errors.New("Agent runtime is not ready"))
	}
	runtime := application.runtimeState.Load()
	if runtime == nil || runtime.store == nil || runtime.inboundDispatch == nil {
		return agent.NewError(agent.ErrorNotReady, "direct invoke", errors.New("conversation runtime is not ready"))
	}
	// A chat the bot has never heard from is created, with the kind and
	// allowlist flag an incoming message from it would get; the turn's
	// permission check still refuses a chat that is not allowlisted.
	kind := conversation.ChatDirect
	if strings.HasSuffix(address, "@g.us") {
		kind = conversation.ChatGroup
	}
	gate, err := policy.NewInboundGate(application.config.OwnerAddress(), application.config.Allowlist())
	if err != nil {
		return agent.NewError(agent.ErrorNotReady, "direct invoke", err)
	}
	chatID, err := runtime.store.Inbound().EnsureChat(ctx, application.config.TenantID(), application.config.AccountID(),
		address, kind, gate.ChatAllowlisted(kind, address), time.Now().UTC())
	if err != nil {
		return err
	}
	key := agent.Key{TenantID: application.config.TenantID(), AccountID: application.config.AccountID(), ChatID: chatID}
	return runtime.inboundDispatch.DirectInvoke(ctx, key, prompt)
}

// startDirectInvoke serves the direct-invoke endpoint when an API key is
// configured. The endpoint is optional: failing to bind it is logged and
// leaves the bot running without it. The returned function stops it.
func (application *Application) startDirectInvoke() func() {
	handler := directinvoke.Handler(directinvoke.Config{
		APIKey: application.config.DirectInvokeAPIKey(), MaxPromptBytes: inbound.MaxTaskPromptBytes, Invoker: application,
	})
	if handler == nil {
		return func() {}
	}
	address := application.config.DirectInvokeAddress()
	listener, err := net.Listen("tcp", address)
	if err != nil {
		application.logger.Error("direct invoke endpoint disabled: cannot listen", "address", address, "error", err)
		return func() {}
	}
	server := &http.Server{
		Handler: handler, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second,
		WriteTimeout: 15 * time.Second, IdleTimeout: 60 * time.Second,
	}
	go func() {
		if err := server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			application.logger.Error("direct invoke endpoint stopped", "error", err)
		}
	}()
	application.logger.Info("direct invoke endpoint started", "address", listener.Addr().String())
	return func() {
		ctx, cancel := context.WithTimeout(context.Background(), application.config.ShutdownTimeout())
		defer cancel()
		if err := server.Shutdown(ctx); err != nil {
			_ = server.Close()
		}
	}
}
