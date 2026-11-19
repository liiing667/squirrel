package squirrel

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakePreparer is a controllable PreparerContext used to exercise cache state
// without a real database. Prepare calls block until release is signaled, so
// concurrency tests can enforce a deterministic interleaving.
type fakePreparer struct {
	mu       sync.Mutex
	started  chan string
	counts   map[string]int
	failWith map[string]error
	gates    map[string][]*prepareGate
	autoFire bool
}

// prepareGate blocks a single prepare until release is called.
type prepareGate struct {
	release chan struct{}
	once    sync.Once
}

func newPrepareGate() *prepareGate {
	return &prepareGate{release: make(chan struct{})}
}

func (g *prepareGate) fire() {
	g.once.Do(func() { close(g.release) })
}

func newFakePreparer() *fakePreparer {
	return &fakePreparer{
		started:  make(chan string, 16),
		counts:   make(map[string]int),
		failWith: make(map[string]error),
		gates:    make(map[string][]*prepareGate),
	}
}

func (f *fakePreparer) prepare(query string) (*sql.Stmt, error) {
	f.mu.Lock()
	f.counts[query]++
	failWith := f.failWith[query]
	gate := newPrepareGate()
	f.gates[query] = append(f.gates[query], gate)
	autoFire := f.autoFire
	f.mu.Unlock()

	select {
	case f.started <- query:
	default:
	}
	if autoFire {
		gate.fire()
	}
	<-gate.release

	if failWith != nil {
		f.mu.Lock()
		delete(f.failWith, query)
		f.mu.Unlock()
		return nil, failWith
	}
	return nil, nil
}

func (f *fakePreparer) Prepare(query string) (*sql.Stmt, error) {
	return f.prepare(query)
}

func (f *fakePreparer) PrepareContext(_ context.Context, query string) (*sql.Stmt, error) {
	return f.prepare(query)
}

func (f *fakePreparer) prepareCount(query string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.counts[query]
}

func (f *fakePreparer) setAutoFire(v bool) {
	f.mu.Lock()
	f.autoFire = v
	f.mu.Unlock()
}

// gateFor returns the gate for the nth prepare (0-based) of query.
func (f *fakePreparer) gateFor(query string, n int) *prepareGate {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.gates[query][n]
}

func TestStmtCacheGenerationAdvancesOnClear(t *testing.T) {
	sc := NewStmtCache(newFakePreparer())

	assert.Equal(t, uint64(0), sc.Generation())
	require.NoError(t, sc.Clear())
	assert.Equal(t, uint64(1), sc.Generation())
	require.NoError(t, sc.Clear())
	assert.Equal(t, uint64(2), sc.Generation())
}

func TestStmtCachePrepareLifecycleAndReuse(t *testing.T) {
	prep := newFakePreparer()
	sc := NewStmtCache(prep)
	query := "SELECT 1"

	assert.Equal(t, StateUnknown, sc.State(query))

	done := make(chan struct{})
	go func() {
		_, _ = sc.Prepare(query)
		close(done)
	}()

	assert.Equal(t, query, <-prep.started)

	// While the prepare is in flight its state must be observable.
	assert.Equal(t, StatePreparing, sc.State(query))
	entry, ok := sc.Entry(query)
	require.True(t, ok)
	assert.Equal(t, StatePreparing, entry.State)

	prep.gateFor(query, 0).fire()
	<-done

	assert.Equal(t, StatePrepared, sc.State(query))
	assert.Equal(t, 1, prep.prepareCount(query))

	// A second call hits the cache and does not prepare again.
	_, err := sc.Prepare(query)
	require.NoError(t, err)
	assert.Equal(t, 1, prep.prepareCount(query))

	snap := sc.Snapshot()
	assert.Equal(t, sc.Generation(), snap.Generation)
	assert.Equal(t, 1, snap.Prepared)
	assert.Equal(t, 0, snap.Preparing)
}

func TestStmtCacheFailedPrepareDoesNotPollute(t *testing.T) {
	prep := newFakePreparer()
	prep.failWith["SELECT boom"] = errors.New("prepare failed")
	sc := NewStmtCache(prep)
	query := "SELECT boom"

	done := make(chan struct{})
	go func() {
		_, err := sc.Prepare(query)
		assert.Error(t, err)
		close(done)
	}()
	<-prep.started
	prep.gateFor(query, 0).fire()
	<-done

	assert.Equal(t, StateFailed, sc.State(query))
	entry, ok := sc.Entry(query)
	require.True(t, ok)
	assert.Equal(t, StateFailed, entry.State)
	assert.Error(t, entry.Err)

	// The failed statement must not be cached: another attempt prepares again.
	done2 := make(chan struct{})
	go func() {
		_, _ = sc.Prepare(query)
		close(done2)
	}()
	<-prep.started
	assert.Equal(t, 2, prep.prepareCount(query))
	prep.gateFor(query, 1).fire()
	<-done2

	assert.Equal(t, StatePrepared, sc.State(query))
}

func TestStmtCacheCanceledContextDoesNotPollute(t *testing.T) {
	prep := newFakePreparer()
	sc := NewStmtCache(prep)
	query := "SELECT canceled"

	// An already-canceled context is observable and never reaches the preparer.
	canceledCtx, cancel := context.WithCancel(context.Background())
	cancel()
	stmt, err := sc.PrepareContext(canceledCtx, query)
	assert.Nil(t, stmt)
	assert.Equal(t, context.Canceled, err)
	assert.Equal(t, StateCanceled, sc.State(query))
	assert.Equal(t, 0, prep.prepareCount(query))

	// A fresh context is allowed to prepare the same query.
	done := make(chan struct{})
	go func() {
		_, _ = sc.PrepareContext(context.Background(), query)
		close(done)
	}()
	<-prep.started
	assert.Equal(t, StatePreparing, sc.State(query))
	prep.gateFor(query, 0).fire()
	<-done

	assert.Equal(t, StatePrepared, sc.State(query))
	assert.Equal(t, 1, prep.prepareCount(query))
}

func TestStmtCacheCachedHitSurvivesCanceledContext(t *testing.T) {
	prep := newFakePreparer()
	sc := NewStmtCache(prep)
	query := "SELECT cached-hit"

	done := make(chan struct{})
	go func() {
		_, _ = sc.PrepareContext(context.Background(), query)
		close(done)
	}()
	<-prep.started
	prep.gateFor(query, 0).fire()
	<-done
	require.Equal(t, StatePrepared, sc.State(query))

	canceledCtx, cancel := context.WithCancel(context.Background())
	cancel()
	stmt, err := sc.PrepareContext(canceledCtx, query)
	require.NoError(t, err)
	assert.Equal(t, 1, prep.prepareCount(query), "cached hit must not re-prepare")
	assert.Equal(t, StatePrepared, sc.State(query))
	_ = stmt
}

func TestStmtCacheConcurrentPreparesShareOnePrepare(t *testing.T) {
	prep := newFakePreparer()
	sc := NewStmtCache(prep)
	query := "SELECT concurrent"

	const waiters = 5
	var wg sync.WaitGroup
	errs := make([]error, waiters)
	wg.Add(waiters)
	for i := 0; i < waiters; i++ {
		go func(idx int) {
			defer wg.Done()
			_, errs[idx] = sc.Prepare(query)
		}(i)
	}

	assert.Equal(t, query, <-prep.started)

	// The single in-flight prepare is shared by every concurrent caller.
	assert.True(t, sc.State(query) == StatePreparing)

	prep.gateFor(query, 0).fire()
	wg.Wait()

	for _, err := range errs {
		assert.NoError(t, err)
	}
	assert.Equal(t, 1, prep.prepareCount(query))
	assert.Equal(t, StatePrepared, sc.State(query))
}

// TestStmtCacheClearDuringPrepareDeterministic pins the exact interleaving
// where a Clear lands while a prepare is in flight. It documents the
// generation transitions and final state callers can rely on.
func TestStmtCacheClearDuringPrepareDeterministic(t *testing.T) {
	prep := newFakePreparer()
	sc := NewStmtCache(prep)
	query := "SELECT generation"

	// Phase 1: an in-flight prepare belongs to generation 0.
	prepareDone := make(chan struct{})
	go func() {
		_, _ = sc.Prepare(query)
		close(prepareDone)
	}()
	assert.Equal(t, query, <-prep.started)
	assert.Equal(t, StatePreparing, sc.State(query))
	assert.Equal(t, uint64(0), sc.Generation())

	// Phase 2: Clear bumps the generation before the prepare returns.
	require.NoError(t, sc.Clear())
	assert.Equal(t, uint64(1), sc.Generation())

	// Phase 3: the stale prepare completes; it must never enter the cache and
	// its outcome is recorded against the new generation as canceled.
	prep.gateFor(query, 0).fire()
	<-prepareDone

	entry, ok := sc.Entry(query)
	require.True(t, ok)
	assert.Equal(t, StateCanceled, entry.State)
	assert.Equal(t, uint64(1), entry.Generation)
	assert.Equal(t, context.Canceled, entry.Err)

	snap := sc.Snapshot()
	assert.Equal(t, uint64(1), snap.Generation)
	assert.Equal(t, 0, snap.Prepared)
	assert.Equal(t, 1, snap.Canceled)

	// Phase 4: a later prepare in the new generation starts fresh and succeeds.
	done2 := make(chan struct{})
	go func() {
		_, _ = sc.Prepare(query)
		close(done2)
	}()
	assert.Equal(t, query, <-prep.started)
	assert.Equal(t, StatePreparing, sc.State(query))
	prep.gateFor(query, 1).fire()
	<-done2

	assert.Equal(t, StatePrepared, sc.State(query))
	assert.Equal(t, 2, prep.prepareCount(query))
	entry, _ = sc.Entry(query)
	assert.Equal(t, uint64(1), entry.Generation)
}

func TestStmtCacheClearThenOldHitRemainsUsableWithinGeneration(t *testing.T) {
	prep := newFakePreparer()
	sc := NewStmtCache(prep)
	query := "SELECT reusable"

	done := make(chan struct{})
	go func() {
		_, _ = sc.Prepare(query)
		close(done)
	}()
	<-prep.started
	prep.gateFor(query, 0).fire()
	<-done
	require.Equal(t, StatePrepared, sc.State(query))

	// Before a Clear, repeated calls keep returning the same cached statement.
	first, err := sc.Prepare(query)
	require.NoError(t, err)
	second, err := sc.Prepare(query)
	require.NoError(t, err)
	assert.True(t, first == second, "expected the same cached statement")
	assert.Equal(t, 1, prep.prepareCount(query))

	// After Clear the old generation is invalidated and a new prepare happens.
	require.NoError(t, sc.Clear())
	assert.Equal(t, uint64(1), sc.Generation())

	done2 := make(chan struct{})
	go func() {
		_, _ = sc.Prepare(query)
		close(done2)
	}()
	<-prep.started
	prep.gateFor(query, 1).fire()
	<-done2

	assert.Equal(t, StatePrepared, sc.State(query))
	assert.Equal(t, 2, prep.prepareCount(query))
}

func TestCacheStateStringStable(t *testing.T) {
	cases := map[CacheState]string{
		StateUnknown:   "unknown",
		StatePreparing: "preparing",
		StatePrepared:  "prepared",
		StateCanceled:  "canceled",
		StateFailed:    "failed",
	}
	for state, name := range cases {
		assert.Equal(t, name, state.String(), fmt.Sprintf("state %d", state))
	}
}

// TestStmtCacheConcurrentPrepareAndClear hammers Prepare and Clear together.
// It must never panic, race, or expose a statement outside its generation.
func TestStmtCacheConcurrentPrepareAndClear(t *testing.T) {
	prep := newFakePreparer()
	prep.setAutoFire(true)
	sc := NewStmtCache(prep)
	queries := []string{"SELECT a", "SELECT b", "SELECT c"}

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				query := queries[(idx+j)%len(queries)]
				_, _ = sc.Prepare(query)
				if j%3 == 0 {
					_ = sc.Clear()
				}
				_ = sc.Snapshot()
			}
		}(i)
	}
	wg.Wait()

	// Every observable entry must reference a generation that actually existed.
	snap := sc.Snapshot()
	for _, entry := range snap.Entries {
		assert.True(t, entry.Generation <= snap.Generation)
		assert.NotEqual(t, StateUnknown, entry.State)
	}
}
