package app

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/Chomosuke9/WazzapAgent-Go/internal/account"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/action"
	llmopenai "github.com/Chomosuke9/WazzapAgent-Go/internal/adapters/llm/openai"
	appsqlite "github.com/Chomosuke9/WazzapAgent-Go/internal/adapters/sqlite"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/adapters/typesafe"
	whatsapp "github.com/Chomosuke9/WazzapAgent-Go/internal/adapters/whatsapp/hypermeow"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/agent"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/command"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/effect"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/identity"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/inbound"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/llm/fallback"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/maintenance"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/observability"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/policy"
)

const (
	maintenanceInterval  = time.Hour
	terminalRetentionAge = 30 * 24 * time.Hour
)

type conversationRuntime struct {
	store            *appsqlite.Store
	configDefaults   agent.ConfigValues
	langSmith        *observability.LangSmith
	account          *account.Runtime
	adapter          *whatsapp.Adapter
	gate             *policy.FixedGate
	dispatcher       *action.Dispatcher
	effectDispatcher *effect.Dispatcher
	inboundDispatch  *inbound.Dispatcher
	maintenance      *maintenance.Worker
	shutdownTimeout  time.Duration
	tenantID         identity.TenantID
	logger           *slog.Logger

	closeMu  sync.Mutex
	closed   bool
	closeErr error
}

func (application *Application) composeRuntime(ctx context.Context) (_ *conversationRuntime, resultErr error) {
	if application.options.SystemPolicy == "" {
		return nil, errors.New("system policy is required for bot runtime")
	}
	if err := prepareDataDir(application.config.TenantDataDir()); err != nil {
		return nil, agent.NewError(agent.ErrorStorageFailure, "prepare tenant data directory", err)
	}
	store, err := appsqlite.Open(ctx, application.config.AppDatabasePath())
	if err != nil {
		return nil, err
	}
	// Register this before every subsequent constructor, including the context
	// builder: failed composition must never leave the SQLite handle open.
	defer func() {
		if resultErr != nil {
			_ = store.Close()
		}
	}()
	// Nothing has been sent in this run yet, so anything still executing was
	// cut off by the last one.
	if err := store.ResolveInterrupted(ctx, application.config.TenantID()); err != nil {
		return nil, err
	}
	defaults := agent.ConfigValues{
		Model:      agent.ModelConfig{ProviderID: application.config.LLMProviderID(), Model: application.config.LLMModel(), MaxOutputTokens: application.config.MaxOutputTokens()},
		Prompt:     application.config.BasePrompt(),
		Permission: agent.PermissionConfig{PolicyID: application.config.PolicyID(), Revision: application.config.PolicyRevision()},
		Triggers:   application.config.ChatDefaults().Triggers(),
	}
	chatDefaults := application.config.ChatDefaults()
	defaults.Permission.ModerationLevel = agent.ModerationLevel(chatDefaults.ModerationLevel)
	defaults.PromptOverride = chatDefaults.PromptOverride()
	if application.config.AgentEnabled() {
		globalDefaults := defaults
		globalDefaults.Permission.ModerationLevel = agent.ModerationNone
		globalDefaults.PromptOverride = nil
		if _, err := store.Configs().ReconcileAccountDefaults(ctx, application.config.TenantID(), application.config.AccountID(), globalDefaults); err != nil {
			return nil, err
		}
		if err := store.Inbound().ReconcileAccountPolicy(ctx, application.config.TenantID(), application.config.AccountID(), application.config.OwnerAddress(), application.config.Allowlist()); err != nil {
			return nil, err
		}
	}
	contextBuilder, err := agent.NewDeterministicContextBuilder(application.config.MaxContextBytes(), application.config.AssistantName())
	if err != nil {
		return nil, err
	}
	langSmith, err := observability.NewLangSmith(application.config.LangSmithAPIKey())
	if err != nil {
		return nil, err
	}
	defer func() {
		if resultErr != nil {
			closeCtx, cancel := context.WithTimeout(context.Background(), application.config.ShutdownTimeout())
			_ = langSmith.Shutdown(closeCtx)
			cancel()
		}
	}()
	llmHTTPClient := langSmith.WrapHTTPClient(&http.Client{Timeout: application.config.LLMTimeout()})
	primaryModel, err := llmopenai.New(llmopenai.Config{
		Endpoint: application.config.LLMEndpoint(), APIKey: application.config.LLMAPIKey(),
		ProviderID: application.config.LLMProviderID(), SystemPolicy: application.options.SystemPolicy,
		Timeout: application.config.LLMTimeout(), Concurrency: application.config.LLMConcurrency(),
		MaxResponseBytes: application.config.MaxResponseBytes(), HTTPClient: llmHTTPClient,
		Observer: application.metrics, Commands: inbound.CommandRegistry(),
	})
	if err != nil {
		return nil, err
	}
	candidates := []fallback.Candidate{{Name: "primary", Model: primaryModel}}
	if application.config.LLMFallbackEndpoint() != "" {
		fallbackModel, fallbackErr := llmopenai.New(llmopenai.Config{
			Endpoint: application.config.LLMFallbackEndpoint(), APIKey: application.config.LLMFallbackAPIKey(),
			ProviderID: application.config.LLMProviderID(), SystemPolicy: application.options.SystemPolicy,
			Timeout: application.config.LLMTimeout(), Concurrency: application.config.LLMConcurrency(),
			MaxResponseBytes: application.config.MaxResponseBytes(), HTTPClient: llmHTTPClient,
			Observer: application.metrics, Commands: inbound.CommandRegistry(),
		})
		if fallbackErr != nil {
			return nil, fallbackErr
		}
		candidates = append(candidates, fallback.Candidate{Name: "fallback-1", Model: fallbackModel})
	}
	model, err := fallback.New(candidates)
	if err != nil {
		return nil, err
	}
	waAdapter, err := whatsapp.Open(ctx, whatsapp.Config{
		TenantID: application.config.TenantID(), AccountID: application.config.AccountID(),
		DeviceStorePath: application.config.WhatsAppDatabasePath(), OwnerAddress: application.config.OwnerAddress(),
		Allowlist: application.config.Allowlist(), QueueCapacity: application.config.InboundQueue(),
		Workers: application.config.InboundWorkers(), ConnectTimeout: application.config.ConnectTimeout(),
		SendTimeout: application.config.SendTimeout(), Pairing: application.options.Pairing,
		Targets: store.Inbound(), GroupNames: store.Inbound(), GroupMetadata: store.Inbound(), Broadcasts: store,
		Stickers: store.Stickers(), Logger: application.logger,
	})
	if err != nil {
		return nil, err
	}
	adapterOwned := true
	defer func() {
		if resultErr != nil && adapterOwned {
			_ = waAdapter.Stop(context.Background())
		}
	}()
	gate, err := policy.NewFixedGate(application.config.PolicyID(), application.config.PolicyRevision(), store.Configs(), store.Inbound(), waAdapter, application.config.AssistantName(), application.config.AgentEnabled())
	if err != nil {
		return nil, err
	}
	if apiKey := application.config.TypeSafeAPIKey(); apiKey != "" {
		client, err := typesafe.New(apiKey, nil)
		if err != nil {
			return nil, err
		}
		gate.SetAddressJudge(typesafe.NewAddressJudge(client, store.History(), application.config.AssistantName(), application.logger))
	}
	dispatcher, err := action.NewDispatcher(store.Actions(), gate, waAdapter, agent.SystemClock{}, application.metrics)
	if err != nil {
		return nil, err
	}
	effectDispatcher, err := effect.NewDispatcher(store.Effects(), gate, waAdapter, agent.SystemClock{})
	if err != nil {
		return nil, err
	}
	agentLogs := observability.NewAgentLogger(application.logger)
	factory := agent.FactoryFunc(func(factoryCtx context.Context, key agent.Key) (*agent.Agent, error) {
		return agent.New(factoryCtx, key, agent.Dependencies{
			Defaults: defaults, ConfigStore: store.Configs(), HistoryStore: store.History(), Turns: store.Turns(),
			Context: contextBuilder, ChatContext: waAdapter, HistoryWindow: application.config.HistoryWindow(),
			Model: model, Responses: dispatcher, Effects: effectDispatcher,
			InvokeEvents: agentLogs, Clock: agent.SystemClock{},
		})
	})
	commandPlatform := command.Platform{
		Text: waAdapter, Buttons: waAdapter, Group: waAdapter,
		Media: waAdapter, Stickers: waAdapter, Catalog: store.Stickers(),
		AssistantName: application.config.AssistantName(),
	}
	registry, err := agent.NewRegistry(factory)
	if err != nil {
		return nil, err
	}
	commandResponses, err := action.NewCommandResponder(store.Actions(), dispatcher)
	if err != nil {
		return nil, err
	}
	inboundDispatch, err := inbound.NewDispatcher(store.Inbound(), registry, gate, commandResponses, application.metrics, commandPlatform, inbound.Options{
		Debounce: application.config.MessageDebounce(), BurstCap: application.config.MessageBurstCap(),
		Activity: waAdapter, Events: agentLogs, ChatContext: waAdapter, Muter: waAdapter, Stickers: store.Stickers(),
		Report: func(err error) {
			application.logger.Error("inbound processing failed", "code", agent.CodeOf(err), "error", err)
		},
	})
	if err != nil {
		return nil, err
	}
	// Commands the model issues schedule tasks on the same dispatcher.
	commandPlatform.Tasks = inboundDispatch
	modelCommands, err := inbound.NewModelCommandExecutor(factory, gate, commandPlatform, application.metrics, agent.SystemClock{})
	if err != nil {
		return nil, err
	}
	if err := effectDispatcher.BindCommandExecutor(modelCommands); err != nil {
		return nil, err
	}
	if err := waAdapter.BindHandler(inboundDispatch); err != nil {
		return nil, err
	}
	maintenanceWorker, err := maintenance.NewWorker(application.config.TenantID(), store, agent.SystemClock{}, maintenanceInterval, terminalRetentionAge, 500,
		agent.RetentionPolicy{KeepLatest: application.config.HistoryKeepLatest(), MaxAge: application.config.HistoryMaxAge()})
	if err != nil {
		return nil, err
	}
	accountRuntime, err := account.NewRuntime(application.config.TenantID(), application.config.AccountID(), waAdapter, application.config.ShutdownTimeout())
	if err != nil {
		return nil, err
	}
	adapterOwned = false
	return &conversationRuntime{store: store, configDefaults: defaults, langSmith: langSmith, account: accountRuntime, adapter: waAdapter,
		gate: gate, dispatcher: dispatcher, effectDispatcher: effectDispatcher, inboundDispatch: inboundDispatch, maintenance: maintenanceWorker,
		tenantID: application.config.TenantID(), logger: application.logger,
		shutdownTimeout: application.config.ShutdownTimeout()}, nil
}

func prepareDataDir(path string) error {
	if err := os.MkdirAll(path, 0o700); err != nil {
		return err
	}
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return errors.New("configured data path is not a directory")
	}
	return nil
}
