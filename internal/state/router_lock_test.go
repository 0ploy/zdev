//go:build darwin || linux

package state

import (
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// ConnectRouter reads state while holding the router lock: the two locks
// must be independent, or every project start deadlocks.
func TestWithRouterLock_AllowsStateAccess(t *testing.T) {
	mgr := NewManager(filepath.Join(t.TempDir(), "state.yaml"))
	done := make(chan error, 1)
	go func() {
		done <- mgr.WithRouterLock(func() error {
			if err := mgr.RegisterProjectWithRouting("p", "/p", nil, nil, []string{"p.0ploy.dev"}); err != nil {
				return err
			}
			_, err := mgr.GetAllRoutedHosts()
			return err
		})
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("state access under the router lock deadlocked")
	}
}

// Two zdev processes updating router aliases must take turns (flock is
// per open file, so goroutines with their own Manager behave like separate
// processes).
func TestWithRouterLock_Serializes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.yaml")
	var inside, maxInside int32
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = NewManager(path).WithRouterLock(func() error {
				n := atomic.AddInt32(&inside, 1)
				for {
					m := atomic.LoadInt32(&maxInside)
					if n <= m || atomic.CompareAndSwapInt32(&maxInside, m, n) {
						break
					}
				}
				time.Sleep(20 * time.Millisecond)
				atomic.AddInt32(&inside, -1)
				return nil
			})
		}()
	}
	wg.Wait()
	if maxInside != 1 {
		t.Errorf("%d callers inside the router lock at once, want 1", maxInside)
	}
}
