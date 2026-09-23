package account

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/Chomosuke9/WazzapAgent-Go/internal/agent"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/identity"
)

type State uint8

const (
	StateStopped State = iota
	StateStarting
	StatePairing
	StateConnecting
	StateOpen
	StateReconnecting
	StateDraining
	StateFailed
)

func (state State) String() string {
	switch state {
	case StateStopped:
		return "stopped"
	case StateStarting:
		return "starting"
	case StatePairing:
		return "pairing"
	case StateConnecting:
		return "connecting"
	case StateOpen:
		return "open"
	case StateReconnecting:
		return "reconnecting"
	case StateDraining:
		return "draining"
	case StateFailed:
		return "failed"
	default:
		return "unknown"
	}
}

type ConnectionEvent struct {
	Connected bool
	Code      string
}

type Connector interface {
	Start(context.Context) error
	Stop(context.Context) error
	Ready() bool
	Events() <-chan ConnectionEvent
	Fatal() <-chan error
}

type Runtime struct {
	tenantID    identity.TenantID
	accountID   identity.AccountID
	connector   Connector
	stopTimeout time.Duration

	mu          sync.RWMutex
	state       State
	stateSince  time.Time
	lastErrCode agent.ErrorCode
}

type Snapshot struct {
	TenantID   identity.TenantID
	AccountID  identity.AccountID
	State      State
	StateSince time.Time
	ErrorCode  agent.ErrorCode
}

func NewRuntime(
	tenantID identity.TenantID,
	accountID identity.AccountID,
	connector Connector,
	stopTimeout time.Duration,
) (*Runtime, error) {
	if tenantID.IsZero() || accountID.IsZero() || connector == nil || stopTimeout <= 0 {
		return nil, agent.NewError(agent.ErrorInvalidArgument, "create account runtime", errors.New("identity, connector, and stop timeout are required"))
	}
	return &Runtime{
		tenantID:    tenantID,
		accountID:   accountID,
		connector:   connector,
		stopTimeout: stopTimeout,
		state:       StateStopped,
		stateSince:  time.Now().UTC(),
	}, nil
}

func (runtime *Runtime) Run(ctx context.Context) error {
	if !runtime.transition(StateStarting, "") {
		return agent.NewError(agent.ErrorConflict, "start account runtime", errors.New("account is not stopped"))
	}
	runtime.setState(StateConnecting, "")
	if err := runtime.connector.Start(ctx); err != nil {
		if ctx.Err() != nil {
			runtime.setState(StateDraining, "")
			stopErr := runtime.stopConnector()
			if stopErr != nil {
				runtime.setState(StateFailed, runtime.stopErrorCode(stopErr))
				return errors.Join(err, stopErr)
			}
			runtime.setState(StateStopped, "")
			return nil
		}
		runtime.setState(StateFailed, agent.CodeOf(err))
		stopErr := runtime.stopConnector()
		if stopErr != nil {
			runtime.setState(StateFailed, runtime.stopErrorCode(stopErr))
			return errors.Join(err, stopErr)
		}
		return err
	}
	if runtime.connector.Ready() {
		runtime.setState(StateOpen, "")
	}

	events := runtime.connector.Events()
	fatal := runtime.connector.Fatal()
	for events != nil || fatal != nil {
		select {
		case <-ctx.Done():
			runtime.setState(StateDraining, "")
			err := runtime.stopConnector()
			if err != nil {
				runtime.setState(StateFailed, runtime.stopErrorCode(err))
				return err
			}
			runtime.setState(StateStopped, "")
			return nil
		case err, open := <-fatal:
			if !open {
				fatal = nil
				continue
			}
			if err == nil {
				continue
			}
			runtime.setState(StateFailed, agent.CodeOf(err))
			stopErr := runtime.stopConnector()
			if stopErr != nil {
				runtime.setState(StateFailed, runtime.stopErrorCode(stopErr))
				return errors.Join(err, stopErr)
			}
			return err
		case event, open := <-events:
			if !open {
				events = nil
				continue
			}
			if event.Connected && runtime.connector.Ready() {
				runtime.setState(StateOpen, "")
			} else if event.Connected {
				runtime.setState(StateConnecting, "")
			} else {
				runtime.setState(StateReconnecting, "")
			}
		}
	}

	// A connector may close both notification channels while it remains usable.
	// Keep waiting for cancellation instead of repeatedly selecting closed cases.
	<-ctx.Done()
	runtime.setState(StateDraining, "")
	err := runtime.stopConnector()
	if err != nil {
		runtime.setState(StateFailed, runtime.stopErrorCode(err))
		return err
	}
	runtime.setState(StateStopped, "")
	return nil
}

func (runtime *Runtime) Ready() bool {
	runtime.mu.RLock()
	state := runtime.state
	runtime.mu.RUnlock()
	return state == StateOpen && runtime.connector.Ready()
}

func (runtime *Runtime) Snapshot() Snapshot {
	runtime.mu.RLock()
	defer runtime.mu.RUnlock()
	return Snapshot{
		TenantID:   runtime.tenantID,
		AccountID:  runtime.accountID,
		State:      runtime.state,
		StateSince: runtime.stateSince,
		ErrorCode:  runtime.lastErrCode,
	}
}

func (runtime *Runtime) transition(next State, code agent.ErrorCode) bool {
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	if runtime.state != StateStopped {
		return false
	}
	runtime.state = next
	runtime.stateSince = time.Now().UTC()
	runtime.lastErrCode = code
	return true
}

func (runtime *Runtime) setState(next State, code agent.ErrorCode) {
	runtime.mu.Lock()
	runtime.state = next
	runtime.stateSince = time.Now().UTC()
	runtime.lastErrCode = code
	runtime.mu.Unlock()
}

func (runtime *Runtime) stopConnector() error {
	ctx, cancel := context.WithTimeout(context.Background(), runtime.stopTimeout)
	defer cancel()
	return runtime.connector.Stop(ctx)
}

func (runtime *Runtime) stopErrorCode(err error) agent.ErrorCode {
	code := agent.CodeOf(err)
	if code == agent.ErrorInternal && errors.Is(err, context.DeadlineExceeded) {
		return agent.ErrorTimeout
	}
	return code
}
