package app

import (
	"context"
	"errors"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/Chomosuke9/WazzapAgent-Go/internal/account"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/action"
	llmopenai "github.com/Chomosuke9/WazzapAgent-Go/internal/adapters/llm/openai"
	appsqlite "github.com/Chomosuke9/WazzapAgent-Go/internal/adapters/sqlite"
	whatsapp "github.com/Chomosuke9/WazzapAgent-Go/internal/adapters/whatsapp/hypermeow"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/agent"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/effect"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/inbound"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/llm/fallback"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/maintenance"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/observability"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/policy"
)

const (
	generationLeaseMargin = 30 * time.Second
	actionLeaseMargin     = 10 * time.Second
	maintenanceInterval   = time.Hour
	terminalContentAge    = 24 * time.Hour
	terminalRetentionAge  = 30 * 24 * time.Hour
)

type conversationRuntime struct {
	store            *appsqlite.Store
	configDefaults   agent.ConfigValues
	registry         *agent.Registry
	langSmith        *observability.LangSmith
	account          *account.Runtime
	adapter          *whatsapp.Adapter
	gate             *policy.FixedGate
	effectDispatcher *effect.Dispatcher
	recovery         *action.RecoveryWorker
	effectRecovery   *effect.RecoveryWorker
	inboundRecovery  *inbound.RecoveryWorker
	inboundDispatch  *inbound.SplitDispatcher
	maintenance      *maintenance.Worker
	shutdownTimeout  time.Duration

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
	store, err := appsqlite.OpenWithOptions(ctx, application.config.AppDatabasePath(), appsqlite.Options{
		GenerationLeaseTTL: application.config.LLMTimeout() + generationLeaseMargin,
		ActionLeaseTTL:     application.config.SendTimeout() + actionLeaseMargin,
	})
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
	var registry *agent.Registry
	defer func() {
		if resultErr != nil && registry != nil {
			_ = registry.Close(context.Background())
		}
	}()
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
		Targets: store.Inbound(), GroupNames: store.Inbound(), GroupMetadata: store.Inbound(), Broadcasts: store, Logger: application.logger,
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
	dispatcher, err := action.NewDispatcher(store.Actions(), gate, waAdapter, agent.SystemClock{}, application.metrics)
	if err != nil {
		return nil, err
	}
	effectDispatcher, err := effect.NewDispatcher(store.Effects(), gate, waAdapter, agent.SystemClock{})
	if err != nil {
		return nil, err
	}
	recovery, err := action.NewRecoveryWorker(application.config.TenantID(), store.Actions(), dispatcher, agent.SystemClock{}, time.Second, 64)
	if err != nil {
		return nil, err
	}
	effectRecovery, err := effect.NewRecoveryWorker(application.config.TenantID(), store.Effects(), effectDispatcher, agent.SystemClock{}, time.Second, 64)
	if err != nil {
		return nil, err
	}
	events := &configEventRelay{}
	agentLogs := observability.NewAgentLogger(application.logger)
	factory := agent.FactoryFunc(func(factoryCtx context.Context, key agent.Key) (*agent.Agent, error) {
		return agent.New(factoryCtx, key, agent.Dependencies{
			Defaults: defaults, ConfigStore: store.Configs(), HistoryStore: store.History(), Turns: store.Turns(),
			Context: contextBuilder, ChatContext: waAdapter, HistoryWindow: application.config.HistoryWindow(),
			Model: model, Responses: dispatcher, Effects: effectDispatcher, Events: events,
			InvokeEvents: agentLogs, Clock: agent.SystemClock{},
		})
	})
	modelCommands, err := inbound.NewModelCommandExecutor(factory, gate, waAdapter, application.metrics, agent.SystemClock{})
	if err != nil {
		return nil, err
	}
	if err := effectDispatcher.BindCommandExecutor(modelCommands); err != nil {
		return nil, err
	}
	registry, err = agent.NewRegistry(ctx, factory, agent.RegistryLimits{MaxLive: application.config.RegistryMaxLive(), IdleTTL: application.config.RegistryIdleTTL(), ConstructionTimeout: application.config.ConstructionTimeout()})
	if err != nil {
		return nil, err
	}
	events.bind(registry)
	commandResponses, err := action.NewCommandResponder(store.Actions(), dispatcher)
	if err != nil {
		return nil, err
	}
	commandHandler, err := inbound.NewCommandHandler(store.Inbound(), registry, gate, commandResponses, application.metrics, waAdapter)
	if err != nil {
		return nil, err
	}
	aiHandler, err := inbound.NewAIHandler(store.Inbound(), registry, gate, commandResponses, application.metrics, waAdapter, inbound.BatchOptions{
		Debounce: application.config.MessageDebounce(), BurstCap: application.config.MessageBurstCap(), Clock: agent.SystemClock{},
		Activity: waAdapter, Events: agentLogs, ChatContext: waAdapter,
	})
	if err != nil {
		return nil, err
	}
	inboundDispatch, err := inbound.NewSplitDispatcher(store.Inbound(), commandHandler, aiHandler, application.metrics,
		application.config.CommandQueue(), application.config.AIQueue(), application.config.CommandWorkers(), application.config.AIWorkers(),
		func(lane inbound.Lane, err error) {
			application.logger.Error("inbound lane processing failed", "lane", lane, "code", agent.CodeOf(err), "error", err)
		})
	if err != nil {
		return nil, err
	}
	if err := inboundDispatch.EnableMuteEnforcement(waAdapter, agent.SystemClock{}); err != nil {
		return nil, err
	}
	if err := waAdapter.BindHandler(inboundDispatch); err != nil {
		return nil, err
	}
	inboundRecovery, err := inbound.NewRecoveryWorker(application.config.TenantID(), store.Inbound(), inboundDispatch, agent.SystemClock{}, 2*time.Second, 5*time.Second, 64)
	if err != nil {
		return nil, err
	}
	maintenanceWorker, err := maintenance.NewWorkerWithHistory(application.config.TenantID(), store, agent.SystemClock{}, maintenanceInterval, terminalContentAge, terminalRetentionAge, 500,
		agent.RetentionPolicy{KeepLatest: application.config.HistoryKeepLatest(), MaxAge: application.config.HistoryMaxAge()})
	if err != nil {
		return nil, err
	}
	accountRuntime, err := account.NewRuntime(application.config.TenantID(), application.config.AccountID(), waAdapter, application.config.ShutdownTimeout())
	if err != nil {
		return nil, err
	}
	adapterOwned = false
	return &conversationRuntime{store: store, configDefaults: defaults, registry: registry, langSmith: langSmith, account: accountRuntime, adapter: waAdapter,
		gate: gate, effectDispatcher: effectDispatcher,
		recovery: recovery, effectRecovery: effectRecovery, inboundRecovery: inboundRecovery, inboundDispatch: inboundDispatch, maintenance: maintenanceWorker,
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

type configEventRelay struct {
	mu       sync.RWMutex
	registry *agent.Registry
}

func (relay *configEventRelay) bind(registry *agent.Registry) {
	relay.mu.Lock()
	relay.registry = registry
	relay.mu.Unlock()
}

func (relay *configEventRelay) TryPublish(event agent.ConfigChanged) bool {
	relay.mu.RLock()
	registry := relay.registry
	relay.mu.RUnlock()
	if registry != nil {
		registry.NotifyConfigChanged(event)
	}
	return true
}
