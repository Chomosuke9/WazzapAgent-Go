package hypermeow

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Chomosuke9/WazzapAgent-Go/internal/agent"
	broadcastmodel "github.com/Chomosuke9/WazzapAgent-Go/internal/broadcast"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/identity"
	whatsmeow "github.com/polymorfa/hypermeow"
	"github.com/polymorfa/hypermeow/proto/waE2E"
	"github.com/polymorfa/hypermeow/types"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

const (
	broadcastGroupHandleTTL = 10 * time.Minute
	maxBroadcastGroups      = 1000
	maxBroadcastPayloadSize = 256 << 10
	maxBroadcastBatchSize   = 100
	maxBroadcastBatchDelay  = 300
	maxBroadcastScheduleAge = 365 * 24 * time.Hour
	broadcastSchedulePoll   = time.Second
)

type BroadcastGroup struct {
	ID   string
	Name string
}

type BroadcastGroupResult struct {
	ID        string
	Name      string
	Sent      bool
	ErrorCode agent.ErrorCode
}

type broadcastGroupHandleSet struct {
	expiresAt time.Time
	groups    map[string]broadcastGroupTarget
}

type broadcastGroupTarget struct {
	address types.JID
	name    string
}

func (adapter *Adapter) ListBroadcastGroups(ctx context.Context) ([]BroadcastGroup, error) {
	if !adapter.Ready() || adapter.client == nil {
		return nil, agent.NewError(agent.ErrorNotReady, "list WhatsApp broadcast groups", errors.New("account is not connected"))
	}
	requestCtx, cancel := context.WithTimeout(ctx, adapter.sendTimeout)
	defer cancel()
	groups, err := adapter.client.GetJoinedGroups(requestCtx)
	if err != nil {
		return nil, nativeEffectError(requestCtx, "list WhatsApp broadcast groups", err)
	}

	set := broadcastGroupHandleSet{
		expiresAt: time.Now().Add(broadcastGroupHandleTTL),
		groups:    make(map[string]broadcastGroupTarget, len(groups)),
	}
	result := make([]BroadcastGroup, 0, len(groups))
	for _, group := range groups {
		if group == nil || group.JID.Server != types.GroupServer || group.JID.IsEmpty() {
			continue
		}
		id, err := identity.NewChatID()
		if err != nil {
			return nil, agent.NewError(agent.ErrorInternal, "create WhatsApp broadcast group handle", errors.New("could not allocate a group handle"))
		}
		name := strings.TrimSpace(group.GroupName.Name)
		if name == "" {
			name = fmt.Sprintf("Unnamed group %d", len(result)+1)
		}
		set.groups[id.String()] = broadcastGroupTarget{address: group.JID.ToNonAD(), name: name}
		result = append(result, BroadcastGroup{ID: id.String(), Name: name})
	}
	adapter.broadcastMu.Lock()
	adapter.broadcastSet = set
	adapter.broadcastMu.Unlock()
	return result, nil
}

func (adapter *Adapter) BroadcastGroups(ctx context.Context, groupIDs []string, format, payload string, batchSize, batchDelaySeconds int) ([]BroadcastGroupResult, error) {
	if !adapter.Ready() || adapter.client == nil {
		return nil, agent.NewError(agent.ErrorNotReady, "send WhatsApp broadcast", errors.New("account is not connected"))
	}
	if len(groupIDs) == 0 || len(groupIDs) > maxBroadcastGroups {
		return nil, agent.NewError(agent.ErrorInvalidArgument, "send WhatsApp broadcast", errors.New("select between one and 1000 groups"))
	}
	if err := validateBroadcastBatch(batchSize, batchDelaySeconds); err != nil {
		return nil, err
	}
	message, err := broadcastMessage(format, payload)
	if err != nil {
		return nil, err
	}
	targets, err := adapter.resolveBroadcastTargets(groupIDs)
	if err != nil {
		return nil, err
	}
	joinedSet, err := adapter.joinedBroadcastGroups(ctx, "verify WhatsApp broadcast groups")
	if err != nil {
		return nil, err
	}
	return adapter.sendBroadcastTargets(ctx, targets, groupIDs, joinedSet, message, batchSize, batchDelaySeconds), nil
}

func (adapter *Adapter) resolveBroadcastTargets(groupIDs []string) ([]broadcastGroupTarget, error) {
	adapter.broadcastMu.Lock()
	set := adapter.broadcastSet
	if !set.expiresAt.After(time.Now()) {
		adapter.broadcastSet = broadcastGroupHandleSet{}
		adapter.broadcastMu.Unlock()
		return nil, agent.NewError(agent.ErrorNotFound, "send WhatsApp broadcast", errors.New("refresh the group list before sending"))
	}
	targets := make([]broadcastGroupTarget, len(groupIDs))
	seen := make(map[string]struct{}, len(groupIDs))
	for index, id := range groupIDs {
		if _, exists := seen[id]; exists {
			adapter.broadcastMu.Unlock()
			return nil, agent.NewError(agent.ErrorInvalidArgument, "send WhatsApp broadcast", errors.New("a group was selected more than once"))
		}
		seen[id] = struct{}{}
		target, exists := set.groups[id]
		if !exists {
			adapter.broadcastMu.Unlock()
			return nil, agent.NewError(agent.ErrorNotFound, "send WhatsApp broadcast", errors.New("refresh the group list before sending"))
		}
		targets[index] = target
	}
	adapter.broadcastMu.Unlock()
	return targets, nil
}

func (adapter *Adapter) joinedBroadcastGroups(ctx context.Context, operation string) (map[types.JID]struct{}, error) {
	checkCtx, cancel := context.WithTimeout(ctx, adapter.sendTimeout)
	defer cancel()
	joined, err := adapter.client.GetJoinedGroups(checkCtx)
	if err != nil {
		return nil, nativeEffectError(checkCtx, operation, err)
	}
	joinedSet := make(map[types.JID]struct{}, len(joined))
	for _, group := range joined {
		if group != nil && group.JID.Server == types.GroupServer && !group.JID.IsEmpty() {
			joinedSet[group.JID.ToNonAD()] = struct{}{}
		}
	}
	return joinedSet, nil
}

func (adapter *Adapter) sendBroadcastTargets(ctx context.Context, targets []broadcastGroupTarget, ids []string, joinedSet map[types.JID]struct{}, message *waE2E.Message, batchSize, batchDelaySeconds int) []BroadcastGroupResult {
	results := make([]BroadcastGroupResult, len(targets))
	for start := 0; start < len(targets); start += batchSize {
		end := start + batchSize
		if end > len(targets) {
			end = len(targets)
		}
		var workers sync.WaitGroup
		for index := start; index < end; index++ {
			index := index
			target := targets[index]
			workers.Add(1)
			go func() {
				defer workers.Done()
				result := BroadcastGroupResult{Name: target.name}
				if index < len(ids) {
					result.ID = ids[index]
				}
				if _, exists := joinedSet[target.address]; !exists {
					result.ErrorCode = agent.ErrorNotFound
					results[index] = result
					return
				}
				if ctx.Err() != nil {
					result.ErrorCode = contextErrorCode(ctx)
					results[index] = result
					return
				}
				sendCtx, sendCancel := context.WithTimeout(ctx, adapter.sendTimeout)
				defer sendCancel()
				stripe := adapter.sendStripe(target.address.String())
				stripe.Lock()
				response, sendErr := adapter.client.SendMessage(sendCtx, target.address, proto.Clone(message).(*waE2E.Message))
				stripe.Unlock()
				if sendErr != nil {
					result.ErrorCode = agent.CodeOf(nativeEffectError(sendCtx, "send WhatsApp broadcast", sendErr))
					if adapter.logger != nil {
						adapter.logger.Warn("WhatsApp broadcast send failed", "chat_name", target.name, "code", result.ErrorCode, "reason", broadcastFailureReason(sendCtx, sendErr))
					}
				} else if response.ID == "" {
					result.ErrorCode = agent.ErrorUnknownOutcome
					if adapter.logger != nil {
						adapter.logger.Warn("WhatsApp broadcast send returned no message ID", "chat_name", target.name, "code", result.ErrorCode, "reason", "empty_message_id")
					}
				} else {
					result.Sent = true
				}
				results[index] = result
			}()
		}
		workers.Wait()
		if end < len(targets) && batchDelaySeconds > 0 {
			timer := time.NewTimer(time.Duration(batchDelaySeconds) * time.Second)
			select {
			case <-timer.C:
			case <-ctx.Done():
				timer.Stop()
			}
		}
	}
	return results
}

func broadcastFailureReason(ctx context.Context, err error) string {
	switch {
	case errors.Is(ctx.Err(), context.DeadlineExceeded), errors.Is(err, context.DeadlineExceeded), errors.Is(err, whatsmeow.ErrMessageTimedOut):
		return "server_ack_timeout"
	case errors.Is(ctx.Err(), context.Canceled), errors.Is(err, context.Canceled):
		return "send_cancelled"
	case errors.Is(err, whatsmeow.ErrServerReturnedError):
		const prefix = "server returned error"
		message := strings.TrimSpace(strings.TrimPrefix(strings.ToLower(err.Error()), prefix))
		if code, parseErr := strconv.Atoi(message); parseErr == nil && code >= 0 {
			return "server_rejected_code_" + strconv.Itoa(code)
		}
		return "server_rejected"
	default:
		return "provider_send_error"
	}
}

func validateBroadcastBatch(batchSize, batchDelaySeconds int) error {
	if batchSize < 1 || batchSize > maxBroadcastBatchSize || batchDelaySeconds < 0 || batchDelaySeconds > maxBroadcastBatchDelay {
		return agent.NewError(agent.ErrorInvalidArgument, "validate WhatsApp broadcast timing", errors.New("batch size must be 1-100 and batch delay must be 0-300 seconds"))
	}
	return nil
}

func (adapter *Adapter) ScheduleBroadcast(ctx context.Context, groupIDs []string, format, payload string, batchSize, batchDelaySeconds int, scheduledAt time.Time) (broadcastmodel.Schedule, error) {
	if !adapter.Ready() || adapter.client == nil {
		return broadcastmodel.Schedule{}, agent.NewError(agent.ErrorNotReady, "schedule WhatsApp broadcast", errors.New("account is not connected"))
	}
	if adapter.broadcasts == nil {
		return broadcastmodel.Schedule{}, agent.NewError(agent.ErrorUnsupported, "schedule WhatsApp broadcast", errors.New("broadcast schedule storage is unavailable"))
	}
	if len(groupIDs) == 0 || len(groupIDs) > maxBroadcastGroups {
		return broadcastmodel.Schedule{}, agent.NewError(agent.ErrorInvalidArgument, "schedule WhatsApp broadcast", errors.New("select between one and 1000 groups"))
	}
	if err := validateBroadcastBatch(batchSize, batchDelaySeconds); err != nil {
		return broadcastmodel.Schedule{}, err
	}
	now := time.Now()
	if !scheduledAt.After(now) || scheduledAt.After(now.Add(maxBroadcastScheduleAge)) {
		return broadcastmodel.Schedule{}, agent.NewError(agent.ErrorInvalidArgument, "schedule WhatsApp broadcast", errors.New("schedule time must be in the future and within one year"))
	}
	if _, err := broadcastMessage(format, payload); err != nil {
		return broadcastmodel.Schedule{}, err
	}
	targets, err := adapter.resolveBroadcastTargets(groupIDs)
	if err != nil {
		return broadcastmodel.Schedule{}, err
	}
	joinedSet, err := adapter.joinedBroadcastGroups(ctx, "verify scheduled WhatsApp broadcast groups")
	if err != nil {
		return broadcastmodel.Schedule{}, err
	}
	storedTargets := make([]broadcastmodel.Target, len(targets))
	for index, target := range targets {
		if _, exists := joinedSet[target.address]; !exists {
			return broadcastmodel.Schedule{}, agent.NewError(agent.ErrorNotFound, "schedule WhatsApp broadcast", errors.New("the selected group list changed; refresh and select groups again"))
		}
		storedTargets[index] = broadcastmodel.Target{Address: target.address.String(), Name: target.name}
	}
	jobID, err := identity.NewChatID()
	if err != nil {
		return broadcastmodel.Schedule{}, agent.NewError(agent.ErrorInternal, "create WhatsApp broadcast schedule", errors.New("could not allocate a schedule ID"))
	}
	now = time.Now().UTC()
	schedule := broadcastmodel.Schedule{
		ID: jobID.String(), ScheduledAt: scheduledAt.UTC(), Format: format, Payload: payload,
		BatchSize: batchSize, BatchDelaySeconds: batchDelaySeconds, Targets: storedTargets,
		Status: broadcastmodel.StatusScheduled, Results: []broadcastmodel.Result{}, CreatedAt: now, UpdatedAt: now,
	}
	if err := adapter.broadcasts.SaveBroadcastSchedule(ctx, schedule); err != nil {
		return broadcastmodel.Schedule{}, err
	}
	return schedule, nil
}

func (adapter *Adapter) ListBroadcastSchedules(ctx context.Context) ([]broadcastmodel.Schedule, error) {
	if adapter.broadcasts == nil {
		return nil, agent.NewError(agent.ErrorUnsupported, "list WhatsApp broadcast schedules", errors.New("broadcast schedule storage is unavailable"))
	}
	return adapter.broadcasts.ListBroadcastSchedules(ctx)
}

func (adapter *Adapter) CancelBroadcastSchedule(ctx context.Context, id string) error {
	if adapter.broadcasts == nil {
		return agent.NewError(agent.ErrorUnsupported, "cancel WhatsApp broadcast schedule", errors.New("broadcast schedule storage is unavailable"))
	}
	if strings.TrimSpace(id) == "" {
		return agent.NewError(agent.ErrorInvalidArgument, "cancel WhatsApp broadcast schedule", errors.New("schedule ID is required"))
	}
	return adapter.broadcasts.CancelBroadcastSchedule(ctx, id)
}

func (adapter *Adapter) broadcastScheduleWorker() {
	defer adapter.wait.Done()
	ctx := adapter.rootCtx
	recoveryCtx, recoveryCancel := context.WithTimeout(context.Background(), 5*time.Second)
	if err := adapter.broadcasts.RecoverInterruptedBroadcastSchedules(recoveryCtx); err != nil {
		adapter.logger.Error("could not recover interrupted WhatsApp broadcast schedules", "code", agent.CodeOf(err))
	}
	recoveryCancel()
	ticker := time.NewTicker(broadcastSchedulePoll)
	defer ticker.Stop()
	for {
		if adapter.Ready() {
			schedule, err := adapter.broadcasts.ClaimDueBroadcastSchedule(ctx, time.Now().UTC())
			if err != nil {
				adapter.logger.Error("could not claim WhatsApp broadcast schedule", "code", agent.CodeOf(err))
			} else if schedule != nil {
				adapter.runBroadcastSchedule(ctx, *schedule)
				continue
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (adapter *Adapter) runBroadcastSchedule(ctx context.Context, schedule broadcastmodel.Schedule) {
	results := make([]broadcastmodel.Result, len(schedule.Targets))
	message, err := broadcastMessage(schedule.Format, schedule.Payload)
	if err != nil {
		for index, target := range schedule.Targets {
			results[index] = broadcastmodel.Result{Name: target.Name, ErrorCode: string(agent.CodeOf(err))}
		}
		adapter.finishBroadcastSchedule(schedule.ID, results, broadcastmodel.StatusFailed)
		return
	}
	joinedSet, err := adapter.joinedBroadcastGroups(ctx, "verify scheduled WhatsApp broadcast groups")
	if err != nil {
		for index, target := range schedule.Targets {
			results[index] = broadcastmodel.Result{Name: target.Name, ErrorCode: string(agent.CodeOf(err))}
		}
		adapter.finishBroadcastSchedule(schedule.ID, results, broadcastmodel.StatusFailed)
		return
	}
	targets := make([]broadcastGroupTarget, len(schedule.Targets))
	for index, target := range schedule.Targets {
		address, parseErr := types.ParseJID(target.Address)
		if parseErr != nil || address.IsEmpty() || address.Server != types.GroupServer {
			for resultIndex, item := range schedule.Targets {
				results[resultIndex] = broadcastmodel.Result{Name: item.Name, ErrorCode: string(agent.ErrorIntegrityFailure)}
			}
			adapter.finishBroadcastSchedule(schedule.ID, results, broadcastmodel.StatusFailed)
			return
		}
		targets[index] = broadcastGroupTarget{address: address.ToNonAD(), name: target.Name}
	}
	result := adapter.sendBroadcastTargets(ctx, targets, nil, joinedSet, message, schedule.BatchSize, schedule.BatchDelaySeconds)
	sent := 0
	for index, item := range result {
		results[index] = broadcastmodel.Result{Name: item.Name, Sent: item.Sent, ErrorCode: string(item.ErrorCode)}
		if item.Sent {
			sent++
		}
	}
	status := broadcastmodel.StatusFailed
	if sent == len(results) {
		status = broadcastmodel.StatusCompleted
	} else if sent > 0 {
		status = broadcastmodel.StatusPartial
	}
	adapter.finishBroadcastSchedule(schedule.ID, results, status)
}

func (adapter *Adapter) finishBroadcastSchedule(id string, results []broadcastmodel.Result, status broadcastmodel.Status) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := adapter.broadcasts.CompleteBroadcastSchedule(ctx, id, status, results); err != nil {
		adapter.logger.Error("could not persist WhatsApp broadcast result", "code", agent.CodeOf(err))
	}
}

func (adapter *Adapter) clearBroadcastHandles() {
	adapter.broadcastMu.Lock()
	adapter.broadcastSet = broadcastGroupHandleSet{}
	adapter.broadcastMu.Unlock()
}

func broadcastMessage(format, payload string) (*waE2E.Message, error) {
	if strings.TrimSpace(payload) == "" || len(payload) > maxBroadcastPayloadSize {
		return nil, agent.NewError(agent.ErrorInvalidArgument, "validate WhatsApp broadcast message", errors.New("message content is empty or too large"))
	}
	switch format {
	case "text":
		return &waE2E.Message{ExtendedTextMessage: &waE2E.ExtendedTextMessage{Text: proto.String(payload)}}, nil
	case "payload":
		message := &waE2E.Message{}
		if err := (protojson.UnmarshalOptions{}).Unmarshal([]byte(payload), message); err != nil {
			return nil, agent.NewError(agent.ErrorInvalidArgument, "validate WhatsApp broadcast payload", errors.New("message JSON must match the WhatsApp waE2E.Message protobuf format"))
		}
		if proto.Size(message) == 0 {
			return nil, agent.NewError(agent.ErrorInvalidArgument, "validate WhatsApp broadcast payload", errors.New("message payload must contain a WhatsApp message field"))
		}
		return message, nil
	default:
		return nil, agent.NewError(agent.ErrorInvalidArgument, "validate WhatsApp broadcast format", errors.New("message format must be text or payload"))
	}
}

func contextErrorCode(ctx context.Context) agent.ErrorCode {
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return agent.ErrorTimeout
	}
	return agent.ErrorCancelled
}
