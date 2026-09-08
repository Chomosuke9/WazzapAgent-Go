package observability_test

import (
	"testing"
	"time"

	"github.com/Chomosuke9/WazzapAgent-Go/internal/agent"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/observability"
)

func TestMetricsRecordsOnlyBoundedNonSensitiveOutcomes(t *testing.T) {
	metrics := observability.NewMetrics()
	metrics.ObserveInboundClaimed()
	metrics.ObserveInboundDuplicate()
	metrics.ObserveInboundIgnored()
	metrics.ObserveModel(25*time.Millisecond, agent.ErrorTimeout)
	metrics.ObserveDelivery(agent.DeliveryUnknownOutcome, agent.ErrorUnknownOutcome)
	snapshot := metrics.Snapshot()
	if snapshot.InboundClaimed != 1 || snapshot.InboundDuplicates != 1 || snapshot.InboundIgnored != 1 {
		t.Fatalf("inbound metrics = %#v", snapshot)
	}
	if snapshot.ModelCalls != 1 || snapshot.ModelFailures != 1 || snapshot.ModelTimeouts != 1 || snapshot.ModelDurationNS == 0 {
		t.Fatalf("model metrics = %#v", snapshot)
	}
	if snapshot.DeliveryDispatch != 1 || snapshot.DeliveryUnknown != 1 || snapshot.DeliveryErrors != 1 {
		t.Fatalf("delivery metrics = %#v", snapshot)
	}
}
