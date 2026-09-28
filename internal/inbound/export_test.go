package inbound

import "time"

// WaitIdle blocks until no chat has queued, waiting, or running work.
func (dispatcher *Dispatcher) WaitIdle() {
	for {
		dispatcher.mu.Lock()
		idle := len(dispatcher.chats) == 0
		dispatcher.mu.Unlock()
		if idle {
			return
		}
		time.Sleep(time.Millisecond)
	}
}

// WaitTasks blocks until no scheduled task is armed or running.
func (dispatcher *Dispatcher) WaitTasks() {
	for {
		dispatcher.mu.Lock()
		idle := len(dispatcher.tasks) == 0
		dispatcher.mu.Unlock()
		if idle {
			return
		}
		time.Sleep(time.Millisecond)
	}
}

// SetMaxPending lowers the per-chat memory bound for a test.
func SetMaxPending(limit int) (restore func()) {
	previous := maxPending
	maxPending = limit
	return func() { maxPending = previous }
}

// NextDailyRun exposes nextDailyRun to tests.
var NextDailyRun = nextDailyRun
