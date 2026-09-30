package discord

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/bwmarrin/discordgo"

	"github.com/Chomosuke9/DiscordAgent-Go/internal/agent"
	broadcastmodel "github.com/Chomosuke9/DiscordAgent-Go/internal/broadcast"
	"github.com/Chomosuke9/DiscordAgent-Go/internal/identity"
)

const (
	broadcastHandleTTL      = 10 * time.Minute
	maxBroadcastChannels    = 1000
	maxBroadcastPayloadSize = 256 << 10
	maxBroadcastBatchSize   = 100
	maxBroadcastBatchDelay  = 300
	maxBroadcastScheduleAge = 365 * 24 * time.Hour
	broadcastSchedulePoll   = time.Second
	maxEmbeds               = 10
)

// BroadcastGroup is a channel the app may broadcast to, behind a short-lived
// handle.
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

type broadcastHandleSet struct {
	expiresAt time.Time
	channels  map[string]broadcastTarget
}

type broadcastTarget struct {
	channelID string
	name      string
}

// ListBroadcastGroups lists the text channels, across every server the bot
// is in, where it may send messages.
func (adapter *Adapter) ListBroadcastGroups(ctx context.Context) ([]BroadcastGroup, error) {
	if !adapter.Ready() {
		return nil, agent.NewError(agent.ErrorNotReady, "list Discord broadcast channels", errors.New("bot is not connected"))
	}
	targets := adapter.sendableChannels(ctx)
	set := broadcastHandleSet{expiresAt: time.Now().Add(broadcastHandleTTL), channels: make(map[string]broadcastTarget, len(targets))}
	result := make([]BroadcastGroup, 0, len(targets))
	for _, target := range targets {
		handle, err := identity.NewChatID()
		if err != nil {
			return nil, agent.NewError(agent.ErrorInternal, "create Discord broadcast handle", errors.New("could not allocate a channel handle"))
		}
		set.channels[handle.String()] = target
		result = append(result, BroadcastGroup{ID: handle.String(), Name: target.name})
	}
	adapter.broadcastMu.Lock()
	adapter.broadcastSet = set
	adapter.broadcastMu.Unlock()
	return result, nil
}

// sendableChannels are the text and announcement channels the bot may send
// to, sorted by name.
func (adapter *Adapter) sendableChannels(ctx context.Context) []broadcastTarget {
	const needed = discordgo.PermissionViewChannel | discordgo.PermissionSendMessages
	type candidate struct {
		channel *discordgo.Channel
		guild   string
	}
	// Copy under the state's lock, then compute permissions, which take the
	// lock again.
	var candidates []candidate
	state := adapter.client.State
	state.RLock()
	for _, guild := range state.Guilds {
		if guild == nil {
			continue
		}
		for _, channel := range guild.Channels {
			if channel != nil && (channel.Type == discordgo.ChannelTypeGuildText || channel.Type == discordgo.ChannelTypeGuildNews) {
				copied := *channel
				copied.GuildID = guild.ID
				candidates = append(candidates, candidate{channel: &copied, guild: guild.Name})
			}
		}
	}
	state.RUnlock()
	var targets []broadcastTarget
	for _, item := range candidates {
		permissions, err := adapter.botPermissions(ctx, item.channel)
		if err != nil || permissions&needed != needed {
			continue
		}
		targets = append(targets, broadcastTarget{channelID: item.channel.ID, name: "#" + item.channel.Name + " · " + item.guild})
	}
	sort.Slice(targets, func(left, right int) bool {
		return strings.ToLower(targets[left].name) < strings.ToLower(targets[right].name)
	})
	return targets
}

func (adapter *Adapter) BroadcastGroups(ctx context.Context, groupIDs []string, format, payload string, batchSize, batchDelaySeconds int) ([]BroadcastGroupResult, error) {
	if !adapter.Ready() {
		return nil, agent.NewError(agent.ErrorNotReady, "send Discord broadcast", errors.New("bot is not connected"))
	}
	if len(groupIDs) == 0 || len(groupIDs) > maxBroadcastChannels {
		return nil, agent.NewError(agent.ErrorInvalidArgument, "send Discord broadcast", errors.New("select between one and 1000 channels"))
	}
	if err := validateBroadcastBatch(batchSize, batchDelaySeconds); err != nil {
		return nil, err
	}
	messages, err := broadcastMessages(format, payload)
	if err != nil {
		return nil, err
	}
	targets, err := adapter.resolveBroadcastTargets(groupIDs)
	if err != nil {
		return nil, err
	}
	return adapter.sendBroadcastTargets(ctx, targets, groupIDs, messages, batchSize, batchDelaySeconds), nil
}

func (adapter *Adapter) resolveBroadcastTargets(groupIDs []string) ([]broadcastTarget, error) {
	adapter.broadcastMu.Lock()
	defer adapter.broadcastMu.Unlock()
	set := adapter.broadcastSet
	if !set.expiresAt.After(time.Now()) {
		adapter.broadcastSet = broadcastHandleSet{}
		return nil, agent.NewError(agent.ErrorNotFound, "send Discord broadcast", errors.New("refresh the channel list before sending"))
	}
	targets := make([]broadcastTarget, len(groupIDs))
	seen := make(map[string]struct{}, len(groupIDs))
	for index, id := range groupIDs {
		if _, exists := seen[id]; exists {
			return nil, agent.NewError(agent.ErrorInvalidArgument, "send Discord broadcast", errors.New("a channel was selected more than once"))
		}
		seen[id] = struct{}{}
		target, exists := set.channels[id]
		if !exists {
			return nil, agent.NewError(agent.ErrorNotFound, "send Discord broadcast", errors.New("refresh the channel list before sending"))
		}
		targets[index] = target
	}
	return targets, nil
}

// sendBroadcastTargets sends to each channel, batchSize channels at a time,
// pausing between batches. Each channel gets its own result.
func (adapter *Adapter) sendBroadcastTargets(ctx context.Context, targets []broadcastTarget, ids []string, messages []*discordgo.MessageSend, batchSize, batchDelaySeconds int) []BroadcastGroupResult {
	results := make([]BroadcastGroupResult, len(targets))
	stillSendable := make(map[string]struct{})
	for _, target := range adapter.sendableChannels(ctx) {
		stillSendable[target.channelID] = struct{}{}
	}
	for start := 0; start < len(targets); start += batchSize {
		end := min(start+batchSize, len(targets))
		var workers sync.WaitGroup
		for index := start; index < end; index++ {
			workers.Add(1)
			go func(index int, target broadcastTarget) {
				defer workers.Done()
				result := BroadcastGroupResult{Name: target.name}
				if index < len(ids) {
					result.ID = ids[index]
				}
				defer func() { results[index] = result }()
				if _, exists := stillSendable[target.channelID]; !exists {
					result.ErrorCode = agent.ErrorNotFound
					return
				}
				for _, message := range messages {
					if ctx.Err() != nil {
						result.ErrorCode = agent.CodeOf(providerError(ctx, "send Discord broadcast", ctx.Err()))
						return
					}
					sendCtx, cancel := context.WithTimeout(ctx, adapter.sendTimeout)
					sent, err := adapter.client.ChannelMessageSendComplex(target.channelID, message, discordgo.WithContext(sendCtx))
					if err != nil {
						result.ErrorCode = agent.CodeOf(providerError(sendCtx, "send Discord broadcast", err))
						cancel()
						adapter.logger.Warn("Discord broadcast send failed", "chat_name", target.name, "code", result.ErrorCode)
						return
					}
					cancel()
					if sent == nil || sent.ID == "" {
						result.ErrorCode = agent.ErrorUnknownOutcome
						return
					}
				}
				result.Sent = true
			}(index, targets[index])
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

func validateBroadcastBatch(batchSize, batchDelaySeconds int) error {
	if batchSize < 1 || batchSize > maxBroadcastBatchSize || batchDelaySeconds < 0 || batchDelaySeconds > maxBroadcastBatchDelay {
		return agent.NewError(agent.ErrorInvalidArgument, "validate Discord broadcast timing", errors.New("batch size must be 1-100 and batch delay must be 0-300 seconds"))
	}
	return nil
}

func (adapter *Adapter) ScheduleBroadcast(ctx context.Context, groupIDs []string, format, payload string, batchSize, batchDelaySeconds int, scheduledAt time.Time) (broadcastmodel.Schedule, error) {
	if !adapter.Ready() {
		return broadcastmodel.Schedule{}, agent.NewError(agent.ErrorNotReady, "schedule Discord broadcast", errors.New("bot is not connected"))
	}
	if adapter.broadcasts == nil {
		return broadcastmodel.Schedule{}, agent.NewError(agent.ErrorUnsupported, "schedule Discord broadcast", errors.New("broadcast schedule storage is unavailable"))
	}
	if len(groupIDs) == 0 || len(groupIDs) > maxBroadcastChannels {
		return broadcastmodel.Schedule{}, agent.NewError(agent.ErrorInvalidArgument, "schedule Discord broadcast", errors.New("select between one and 1000 channels"))
	}
	if err := validateBroadcastBatch(batchSize, batchDelaySeconds); err != nil {
		return broadcastmodel.Schedule{}, err
	}
	now := time.Now()
	if !scheduledAt.After(now) || scheduledAt.After(now.Add(maxBroadcastScheduleAge)) {
		return broadcastmodel.Schedule{}, agent.NewError(agent.ErrorInvalidArgument, "schedule Discord broadcast", errors.New("schedule time must be in the future and within one year"))
	}
	if _, err := broadcastMessages(format, payload); err != nil {
		return broadcastmodel.Schedule{}, err
	}
	targets, err := adapter.resolveBroadcastTargets(groupIDs)
	if err != nil {
		return broadcastmodel.Schedule{}, err
	}
	storedTargets := make([]broadcastmodel.Target, len(targets))
	for index, target := range targets {
		storedTargets[index] = broadcastmodel.Target{Address: target.channelID, Name: target.name}
	}
	jobID, err := identity.NewChatID()
	if err != nil {
		return broadcastmodel.Schedule{}, agent.NewError(agent.ErrorInternal, "create Discord broadcast schedule", errors.New("could not allocate a schedule ID"))
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
		return nil, agent.NewError(agent.ErrorUnsupported, "list Discord broadcast schedules", errors.New("broadcast schedule storage is unavailable"))
	}
	return adapter.broadcasts.ListBroadcastSchedules(ctx)
}

func (adapter *Adapter) CancelBroadcastSchedule(ctx context.Context, id string) error {
	if adapter.broadcasts == nil {
		return agent.NewError(agent.ErrorUnsupported, "cancel Discord broadcast schedule", errors.New("broadcast schedule storage is unavailable"))
	}
	if strings.TrimSpace(id) == "" {
		return agent.NewError(agent.ErrorInvalidArgument, "cancel Discord broadcast schedule", errors.New("schedule ID is required"))
	}
	return adapter.broadcasts.CancelBroadcastSchedule(ctx, id)
}

func (adapter *Adapter) broadcastScheduleWorker() {
	defer adapter.wait.Done()
	ctx := adapter.rootCtx
	recoveryCtx, recoveryCancel := context.WithTimeout(context.Background(), 5*time.Second)
	if err := adapter.broadcasts.RecoverInterruptedBroadcastSchedules(recoveryCtx); err != nil {
		adapter.logger.Error("could not recover interrupted Discord broadcast schedules", "code", agent.CodeOf(err), "error", err)
	}
	recoveryCancel()
	ticker := time.NewTicker(broadcastSchedulePoll)
	defer ticker.Stop()
	for {
		if adapter.Ready() {
			schedule, err := adapter.broadcasts.ClaimDueBroadcastSchedule(ctx, time.Now().UTC())
			if err != nil {
				adapter.logger.Error("could not claim Discord broadcast schedule", "code", agent.CodeOf(err), "error", err)
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
	failAll := func(code agent.ErrorCode) {
		for index, target := range schedule.Targets {
			results[index] = broadcastmodel.Result{Name: target.Name, ErrorCode: string(code)}
		}
		adapter.finishBroadcastSchedule(schedule.ID, results, broadcastmodel.StatusFailed)
	}
	messages, err := broadcastMessages(schedule.Format, schedule.Payload)
	if err != nil {
		failAll(agent.CodeOf(err))
		return
	}
	targets := make([]broadcastTarget, len(schedule.Targets))
	for index, target := range schedule.Targets {
		if _, err := identity.ParseUserID(target.Address); err != nil {
			failAll(agent.ErrorIntegrityFailure)
			return
		}
		targets[index] = broadcastTarget{channelID: target.Address, name: target.Name}
	}
	sent := 0
	for index, item := range adapter.sendBroadcastTargets(ctx, targets, nil, messages, schedule.BatchSize, schedule.BatchDelaySeconds) {
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
		adapter.logger.Error("could not persist Discord broadcast result", "code", agent.CodeOf(err), "error", err)
	}
}

// broadcastMessages builds what a broadcast sends to each channel: plain
// text, split into parts Discord accepts, or one message from a Discord
// message JSON payload. Broadcasts never ping anyone unless the payload asks
// for it with allowed_mentions.
func broadcastMessages(format, payload string) ([]*discordgo.MessageSend, error) {
	if strings.TrimSpace(payload) == "" || len(payload) > maxBroadcastPayloadSize {
		return nil, agent.NewError(agent.ErrorInvalidArgument, "validate Discord broadcast message", errors.New("message content is empty or too large"))
	}
	noPings := func() *discordgo.MessageAllowedMentions {
		return &discordgo.MessageAllowedMentions{Parse: []discordgo.AllowedMentionType{}}
	}
	switch format {
	case "text":
		parts := splitMessage(payload, maxMessageUnits)
		messages := make([]*discordgo.MessageSend, len(parts))
		for index, part := range parts {
			messages[index] = &discordgo.MessageSend{Content: part, AllowedMentions: noPings()}
		}
		return messages, nil
	case "payload":
		message, err := decodeBroadcastPayload([]byte(payload))
		if err != nil {
			return nil, err
		}
		if message.AllowedMentions == nil {
			message.AllowedMentions = noPings()
		}
		return []*discordgo.MessageSend{message}, nil
	default:
		return nil, agent.NewError(agent.ErrorInvalidArgument, "validate Discord broadcast format", errors.New("message format must be text or payload"))
	}
}

// broadcastPayload is the part of Discord's message JSON a broadcast may
// set. Components are decoded through discordgo's message type, which knows
// every component kind.
type broadcastPayload struct {
	Content         string                            `json:"content,omitempty"`
	Embeds          []*discordgo.MessageEmbed         `json:"embeds,omitempty"`
	Components      json.RawMessage                   `json:"components,omitempty"`
	TTS             bool                              `json:"tts,omitempty"`
	Flags           discordgo.MessageFlags            `json:"flags,omitempty"`
	AllowedMentions *discordgo.MessageAllowedMentions `json:"allowed_mentions,omitempty"`
	Poll            *discordgo.Poll                   `json:"poll,omitempty"`
}

func decodeBroadcastPayload(data []byte) (*discordgo.MessageSend, error) {
	invalid := func(err error) error {
		return agent.NewError(agent.ErrorInvalidArgument, "validate Discord broadcast payload", err)
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var payload broadcastPayload
	if err := decoder.Decode(&payload); err != nil {
		return nil, invalid(fmt.Errorf("the JSON must be a Discord message with content, embeds, components, tts, flags, allowed_mentions or poll: %w", err))
	}
	if decoder.More() {
		return nil, invalid(errors.New("the JSON holds more than one message"))
	}
	message := &discordgo.MessageSend{Content: payload.Content, Embeds: payload.Embeds, TTS: payload.TTS, Flags: payload.Flags, AllowedMentions: payload.AllowedMentions, Poll: payload.Poll}
	if len(payload.Components) > 0 && !bytes.Equal(bytes.TrimSpace(payload.Components), []byte("null")) {
		var holder discordgo.Message
		if err := json.Unmarshal([]byte(`{"components":`+string(payload.Components)+`}`), &holder); err != nil {
			return nil, invalid(fmt.Errorf("components are invalid: %w", err))
		}
		message.Components = holder.Components
	}
	if strings.TrimSpace(message.Content) == "" && len(message.Embeds) == 0 && len(message.Components) == 0 && message.Poll == nil {
		return nil, invalid(errors.New("the message needs content, an embed, components or a poll"))
	}
	if utf16Units(message.Content) > maxMessageUnits {
		return nil, invalid(fmt.Errorf("content is longer than Discord's %d characters", maxMessageUnits))
	}
	if len(message.Embeds) > maxEmbeds {
		return nil, invalid(fmt.Errorf("a message holds at most %d embeds", maxEmbeds))
	}
	return message, nil
}

// NormalizeBroadcastPayload accepts a Discord message JSON, alone or in a
// fenced code block, and returns it formatted, or explains why it is not a
// message a broadcast can send.
func NormalizeBroadcastPayload(input string) (string, error) {
	if len(input) > 2*maxBroadcastPayloadSize {
		return "", errors.New("Input is too large to normalize. Keep the raw message below 512 KiB.")
	}
	if !utf8.ValidString(input) {
		return "", errors.New("Input contains invalid UTF-8 text.")
	}
	candidate := strings.TrimSpace(input)
	if strings.HasPrefix(candidate, "```") {
		candidate = strings.TrimPrefix(candidate, "```")
		if newline := strings.IndexByte(candidate, '\n'); newline >= 0 {
			candidate = candidate[newline+1:]
		}
		candidate = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(candidate), "```"))
	}
	if !json.Valid([]byte(candidate)) {
		return "", errors.New("Invalid JSON: paste a Discord message object, such as {\"content\": \"Hello\"}.")
	}
	if _, err := decodeBroadcastPayload([]byte(candidate)); err != nil {
		return "", errors.New(strings.TrimPrefix(err.Error(), "validate Discord broadcast payload: "))
	}
	var formatted bytes.Buffer
	if err := json.Indent(&formatted, []byte(candidate), "", "  "); err != nil {
		return "", fmt.Errorf("could not format the Discord message: %w", err)
	}
	if formatted.Len() > maxBroadcastPayloadSize {
		return "", errors.New("The normalized message is larger than the 256 KiB broadcast limit.")
	}
	return formatted.String(), nil
}
