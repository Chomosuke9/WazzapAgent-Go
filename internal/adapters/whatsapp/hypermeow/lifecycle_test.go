package hypermeow

import (
	"context"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/Chomosuke9/WazzapAgent-Go/internal/agent"
)

func TestStopTimeoutRetainsStoreUntilWorkersExit(t *testing.T) {
	store, err := openDeviceStore(context.Background(), filepath.Join(t.TempDir(), "device.db"), nil)
	if err != nil {
		t.Fatal(err)
	}
	adapter := &Adapter{container: store}
	adapter.wait.Add(1)
	var release sync.Once
	t.Cleanup(func() {
		release.Do(adapter.wait.Done)
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = adapter.Stop(ctx)
	})

	for attempt := 0; attempt < 2; attempt++ {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
		err := adapter.Stop(ctx)
		cancel()
		if !agent.IsCode(err, agent.ErrorTimeout) {
			t.Fatalf("stop %d = %v, want timeout", attempt, err)
		}
		if _, err := store.GetFirstDevice(context.Background()); err != nil {
			t.Fatalf("store closed while worker still owns it: %v", err)
		}
	}
	if !adapter.closed.Load() {
		t.Fatal("stopping adapter permits a new Start")
	}
	// A late Connected callback cannot reopen a stopping adapter.
	adapter.ready.Store(true)
	if adapter.Ready() {
		t.Fatal("stopping adapter became ready after a late connection event")
	}
	release.Do(adapter.wait.Done)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	var callers sync.WaitGroup
	for range 8 {
		callers.Add(1)
		go func() {
			defer callers.Done()
			if err := adapter.Stop(ctx); err != nil {
				t.Errorf("repeated stop: %v", err)
			}
		}()
	}
	callers.Wait()
	if _, err := store.GetFirstDevice(context.Background()); err == nil {
		t.Fatal("device store remains open after workers exit")
	}
}
