package app

import (
	"encoding/json"
	"fmt"
	"net/http"
	"runtime"
	"time"

	"github.com/Chomosuke9/WazzapAgent-Go/internal/account"
)

type healthResponse struct {
	Status       string `json:"status"`
	AccountState string `json:"account_state,omitempty"`
	ErrorCode    string `json:"error_code,omitempty"`
}

// Handler exposes the diagnostics surface used by the CLI. It is also useful
// to embedders which explicitly choose to serve diagnostics themselves; Run
// never starts an HTTP server implicitly.
func (application *Application) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health/live", func(writer http.ResponseWriter, _ *http.Request) {
		writeHealth(writer, http.StatusOK, healthResponse{Status: "live"})
	})
	mux.HandleFunc("GET /health/ready", func(writer http.ResponseWriter, _ *http.Request) {
		response := healthResponse{Status: "ready"}
		ready := application.Ready()
		if accountRuntime := application.accountState.Load(); accountRuntime != nil {
			snapshot := accountRuntime.Snapshot()
			response.AccountState = snapshot.State.String()
			response.ErrorCode = string(snapshot.ErrorCode)
			ready = ready && accountRuntime.Ready()
		}
		if !ready {
			response.Status = "not_ready"
			writeHealth(writer, http.StatusServiceUnavailable, response)
			return
		}
		writeHealth(writer, http.StatusOK, response)
	})
	mux.HandleFunc("GET /metrics", func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "text/plain; version=0.0.4")
		writer.Header().Set("Cache-Control", "no-store")
		snapshot := application.metrics.Snapshot()
		queueDepth, queueCapacity := 0, 0
		if adapter := application.adapterState.Load(); adapter != nil {
			queueDepth, queueCapacity = adapter.QueueUsage()
		}
		_, _ = fmt.Fprintf(writer, `# TYPE wazzap_inbound_claimed_total counter
wazzap_inbound_claimed_total %d
# TYPE wazzap_inbound_duplicate_total counter
wazzap_inbound_duplicate_total %d
# TYPE wazzap_inbound_ignored_total counter
wazzap_inbound_ignored_total %d
# TYPE wazzap_inbound_batches_total counter
wazzap_inbound_batches_total %d
# TYPE wazzap_inbound_batched_messages_total counter
wazzap_inbound_batched_messages_total %d
# TYPE wazzap_history_resets_total counter
wazzap_history_resets_total %d
# TYPE wazzap_model_calls_total counter
wazzap_model_calls_total %d
# TYPE wazzap_model_failures_total counter
wazzap_model_failures_total %d
# TYPE wazzap_model_timeouts_total counter
wazzap_model_timeouts_total %d
# TYPE wazzap_model_duration_seconds_sum counter
wazzap_model_duration_seconds_sum %.6f
# TYPE wazzap_delivery_dispatch_total counter
wazzap_delivery_dispatch_total %d
# TYPE wazzap_delivery_pending_total counter
wazzap_delivery_pending_total %d
# TYPE wazzap_delivery_succeeded_total counter
wazzap_delivery_succeeded_total %d
# TYPE wazzap_delivery_failed_total counter
wazzap_delivery_failed_total %d
# TYPE wazzap_delivery_unknown_total counter
wazzap_delivery_unknown_total %d
# TYPE wazzap_delivery_errors_total counter
wazzap_delivery_errors_total %d
# TYPE wazzap_inbound_queue_depth gauge
wazzap_inbound_queue_depth %d
# TYPE wazzap_inbound_queue_capacity gauge
wazzap_inbound_queue_capacity %d
# TYPE wazzap_process_goroutines gauge
wazzap_process_goroutines %d
`, snapshot.InboundClaimed, snapshot.InboundDuplicates, snapshot.InboundIgnored,
			snapshot.InboundBatches, snapshot.InboundBatchedMessages, snapshot.HistoryResets,
			snapshot.ModelCalls, snapshot.ModelFailures, snapshot.ModelTimeouts,
			float64(snapshot.ModelDurationNS)/float64(time.Second), snapshot.DeliveryDispatch,
			snapshot.DeliveryPending, snapshot.DeliverySucceeded, snapshot.DeliveryFailed,
			snapshot.DeliveryUnknown, snapshot.DeliveryErrors, queueDepth, queueCapacity, runtime.NumGoroutine())
	})
	return mux
}

func (application *Application) Ready() bool {
	if !application.ready.Load() {
		return false
	}
	application.mu.Lock()
	stopping := application.runStopping
	application.mu.Unlock()
	select {
	case <-stopping:
		return false
	default:
	}
	if accountRuntime := application.accountState.Load(); accountRuntime != nil {
		return accountRuntime.Ready()
	}
	return true
}

// Started reports that runtime composition completed and the process-owned
// worker lifecycle began. It is distinct from Ready, which also requires an
// open WhatsApp connection.
func (application *Application) Started() bool {
	return application != nil && application.ready.Load()
}

// WhatsAppSnapshot returns account connection state without exposing client or
// provider data. The boolean is false when no account runtime was composed.
func (application *Application) WhatsAppSnapshot() (account.Snapshot, bool) {
	if application == nil {
		return account.Snapshot{}, false
	}
	runtime := application.accountState.Load()
	if runtime == nil {
		return account.Snapshot{}, false
	}
	return runtime.Snapshot(), true
}

func writeHealth(writer http.ResponseWriter, status int, response healthResponse) {
	writer.Header().Set("Content-Type", "application/json")
	writer.Header().Set("Cache-Control", "no-store")
	writer.WriteHeader(status)
	_ = json.NewEncoder(writer).Encode(response)
}
