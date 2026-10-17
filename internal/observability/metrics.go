package observability

import (
	"sync/atomic"
	"time"

	"github.com/Chomosuke9/WazzapAgent-Go/internal/agent"
)

type Metrics struct {
	inboundClaimed         atomic.Uint64
	inboundDuplicates      atomic.Uint64
	inboundIgnored         atomic.Uint64
	inboundBatches         atomic.Uint64
	inboundBatchedMessages atomic.Uint64
	historyResets          atomic.Uint64
	modelCalls             atomic.Uint64
	modelFailures          atomic.Uint64
	modelTimeouts          atomic.Uint64
	modelDurationNS        atomic.Uint64
	deliveryDispatch       atomic.Uint64
	deliveryPending        atomic.Uint64
	deliverySucceeded      atomic.Uint64
	deliveryFailed         atomic.Uint64
	deliveryUnknown        atomic.Uint64
	deliveryErrors         atomic.Uint64
}

type MetricsSnapshot struct {
	InboundClaimed         uint64
	InboundDuplicates      uint64
	InboundIgnored         uint64
	InboundBatches         uint64
	InboundBatchedMessages uint64
	HistoryResets          uint64
	ModelCalls             uint64
	ModelFailures          uint64
	ModelTimeouts          uint64
	ModelDurationNS        uint64
	DeliveryDispatch       uint64
	DeliveryPending        uint64
	DeliverySucceeded      uint64
	DeliveryFailed         uint64
	DeliveryUnknown        uint64
	DeliveryErrors         uint64
}

func NewMetrics() *Metrics { return &Metrics{} }

func (metrics *Metrics) ObserveInboundClaimed()   { metrics.inboundClaimed.Add(1) }
func (metrics *Metrics) ObserveInboundDuplicate() { metrics.inboundDuplicates.Add(1) }
func (metrics *Metrics) ObserveInboundIgnored()   { metrics.inboundIgnored.Add(1) }
func (metrics *Metrics) ObserveInboundBatch(size uint32) {
	metrics.inboundBatches.Add(1)
	metrics.inboundBatchedMessages.Add(uint64(size))
}
func (metrics *Metrics) ObserveHistoryReset() { metrics.historyResets.Add(1) }

func (metrics *Metrics) ObserveModel(duration time.Duration, code agent.ErrorCode) {
	metrics.modelCalls.Add(1)
	if duration > 0 {
		metrics.modelDurationNS.Add(uint64(duration))
	}
	if code != "" {
		metrics.modelFailures.Add(1)
	}
	if code == agent.ErrorTimeout {
		metrics.modelTimeouts.Add(1)
	}
}

func (metrics *Metrics) ObserveDelivery(status agent.DeliveryStatus, code agent.ErrorCode) {
	metrics.deliveryDispatch.Add(1)
	switch status {
	case agent.DeliveryPending:
		metrics.deliveryPending.Add(1)
	case agent.DeliverySucceeded:
		metrics.deliverySucceeded.Add(1)
	case agent.DeliveryFailedTerminal:
		metrics.deliveryFailed.Add(1)
	case agent.DeliveryUnknownOutcome:
		metrics.deliveryUnknown.Add(1)
	}
	if code != "" {
		metrics.deliveryErrors.Add(1)
	}
}

func (metrics *Metrics) Snapshot() MetricsSnapshot {
	if metrics == nil {
		return MetricsSnapshot{}
	}
	return MetricsSnapshot{
		InboundClaimed: metrics.inboundClaimed.Load(), InboundDuplicates: metrics.inboundDuplicates.Load(),
		InboundIgnored: metrics.inboundIgnored.Load(), ModelCalls: metrics.modelCalls.Load(),
		InboundBatches: metrics.inboundBatches.Load(), InboundBatchedMessages: metrics.inboundBatchedMessages.Load(),
		HistoryResets: metrics.historyResets.Load(),
		ModelFailures: metrics.modelFailures.Load(), ModelTimeouts: metrics.modelTimeouts.Load(),
		ModelDurationNS: metrics.modelDurationNS.Load(), DeliveryDispatch: metrics.deliveryDispatch.Load(),
		DeliveryPending: metrics.deliveryPending.Load(), DeliverySucceeded: metrics.deliverySucceeded.Load(),
		DeliveryFailed: metrics.deliveryFailed.Load(), DeliveryUnknown: metrics.deliveryUnknown.Load(),
		DeliveryErrors: metrics.deliveryErrors.Load(),
	}
}
