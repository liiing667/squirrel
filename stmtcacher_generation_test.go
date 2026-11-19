package squirrel

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var errExplicit = errors.New("explicit prepare failure")

// waitFor polls cond until it returns true or the deadline passes.
func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("timed out waiting for condition")
}

// fakeDriver is a self-contained database/sql/driver used by the cache tests.
// It records prepares and statement closes, and supports per-query gating so
// concurrent scenarios execute in a fixed order.
var fakeDriverRegistry = struct {
	sync.Mutex
	active *fakeDriver
}{}

type fakeDriver struct {
	mu       sync.Mutex
	prepares int
	stmts    map[string][]*fakeStmt
	err      error
	started  map[string]chan struct{}
	release  map[string]chan struct{}
}

func (d *fakeDriver) Open(name string) (driver.Conn, error) {
	return &fakeConn{drv: d}, nil
}

func (d *fakeDriver) gateQuery(query string) (started, release chan struct{}) {
	started = make(chan struct{}, 8)
	release = make(chan struct{})
	d.mu.Lock()
	d.started[query] = started
	d.release[query] = release
	d.mu.Unlock()
	return started, release
}

func (d *fakeDriver) prepare(query string) (driver.Stmt, error) {
	d.mu.Lock()
	d.prepares++
	err := d.err
	started := d.started[query]
	release := d.release[query]
	stmt := &fakeStmt{query: query}
	d.stmts[query] = append(d.stmts[query], stmt)
	d.mu.Unlock()

	if started != nil {
		started <- struct{}{}
	}
	if release != nil {
		<-release
	}
	if err != nil {
		return nil, err
	}
	return stmt, nil
}

func (d *fakeDriver) prepareCount() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.prepares
}

func (d *fakeDriver) statements(query string) []*fakeStmt {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := make([]*fakeStmt, len(d.stmts[query]))
	copy(out, d.stmts[query])
	return out
}

type fakeConn struct{ drv *fakeDriver }

func (c *fakeConn) Prepare(query string) (driver.Stmt, error) {
	return c.drv.prepare(query)
}

func (c *fakeConn) Close() error              { return nil }
func (c *fakeConn) Begin() (driver.Tx, error) { return nil, errors.New("not implemented") }

type fakeStmt struct {
	query  string
	mu     sync.Mutex
	closed bool
}

func (s *fakeStmt) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = true
	return nil
}

func (s *fakeStmt) NumInput() int                              { return -1 }
func (s *fakeStmt) Exec([]driver.Value) (driver.Result, error) { return nil, nil }
func (s *fakeStmt) Query([]driver.Value) (driver.Rows, error)  { return nil, nil }
func (s *fakeStmt) isClosed() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.closed
}

const fakeDriverName = "squirrel_stmtcache_fake"

func init() {
	sql.Register(fakeDriverName, driverWithRegistry{})
}

type driverWithRegistry struct{}

func (driverWithRegistry) Open(string) (driver.Conn, error) {
	fakeDriverRegistry.Lock()
	drv := fakeDriverRegistry.active
	fakeDriverRegistry.Unlock()
	return &fakeConn{drv: drv}, nil
}

// newFakeDB installs a fresh fakeDriver and returns a *sql.DB backed by it.
func newFakeDB(t *testing.T) (*sql.DB, *fakeDriver) {
	t.Helper()
	drv := &fakeDriver{
		stmts:   make(map[string][]*fakeStmt),
		started: make(map[string]chan struct{}),
		release: make(map[string]chan struct{}),
	}
	fakeDriverRegistry.Lock()
	fakeDriverRegistry.active = drv
	fakeDriverRegistry.Unlock()

	db, err := sql.Open(fakeDriverName, "")
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })
	return db, drv
}

// errorPreparer fails prepares while err is set, then delegates to a real DB.
type errorPreparer struct {
	db  *sql.DB
	err error
}

func (e *errorPreparer) Prepare(query string) (*sql.Stmt, error) {
	if e.err != nil {
		return nil, e.err
	}
	return e.db.Prepare(query)
}

func (e *errorPreparer) PrepareContext(ctx context.Context, query string) (*sql.Stmt, error) {
	if e.err != nil {
		return nil, e.err
	}
	return e.db.PrepareContext(ctx, query)
}

func TestStmtCacheGenerationBumpsOnClear(t *testing.T) {
	db, _ := newFakeDB(t)
	sc := NewStmtCache(db)

	assert.Equal(t, uint64(0), sc.Generation())
	require.NoError(t, sc.Clear())
	assert.Equal(t, uint64(1), sc.Generation())
	require.NoError(t, sc.Clear())
	assert.Equal(t, uint64(2), sc.Generation())
}

func TestStmtCacheFailedPrepareDoesNotPollute(t *testing.T) {
	db, drv := newFakeDB(t)
	failing := &errorPreparer{db: db, err: errExplicit}
	sc := NewStmtCache(failing)
	query := "SELECT boom"

	stmt, err := sc.Prepare(query)
	assert.Nil(t, stmt)
	assert.Equal(t, errExplicit, err)

	state, ok := sc.State(query)
	require.True(t, ok)
	assert.Equal(t, StmtStatusFailed, state.Status)
	assert.Equal(t, errExplicit, state.Err)
	assert.Equal(t, uint64(0), state.Generation)

	// A second attempt must prepare again: the failure was not cached.
	_, err = sc.Prepare(query)
	assert.Equal(t, errExplicit, err)
	state, ok = sc.State(query)
	require.True(t, ok)
	assert.Equal(t, StmtStatusFailed, state.Status)
	assert.NotContains(t, sc.cache, query)

	// Once the preparer recovers, the query is cached normally and a following
	// call is served from the cache without re-preparing.
	failing.err = nil
	stmt, err = sc.Prepare(query)
	require.NoError(t, err)
	assert.NotNil(t, stmt)

	state, ok = sc.State(query)
	require.True(t, ok)
	assert.Equal(t, StmtStatusPrepared, state.Status)

	_, err = sc.Prepare(query)
	require.NoError(t, err)
	state, _ = sc.State(query)
	assert.Equal(t, StmtStatusCacheHit, state.Status)
	assert.Equal(t, uint64(0), state.Generation)
	assert.Equal(t, 1, drv.prepareCount(), "only the recovered attempt reaches the driver")
}

func TestStmtCacheEmptyQueryIsLegitimateAndCached(t *testing.T) {
	db := &DBStub{}
	sc := NewStmtCache(db)
	query := ""

	_, err := sc.Prepare(query)
	require.NoError(t, err)
	_, err = sc.Prepare(query)
	require.NoError(t, err)

	assert.Equal(t, 1, db.PrepareCount, "empty query is a legal key and must hit the cache")
	state, ok := sc.State(query)
	require.True(t, ok)
	assert.Equal(t, StmtStatusCacheHit, state.Status)
}

func TestStmtCacheClearOldHitPreparesFreshGeneration(t *testing.T) {
	db := &DBStub{}
	sc := NewStmtCache(db)
	query := "SELECT 1"

	_, err := sc.Prepare(query)
	require.NoError(t, err)
	state, _ := sc.State(query)
	assert.Equal(t, StmtStatusPrepared, state.Status)
	assert.Equal(t, uint64(0), state.Generation)

	require.NoError(t, sc.Clear())
	assert.Equal(t, uint64(1), sc.Generation())

	// Old cache hits are valid within their generation; after Clear the entry is
	// gone, so this reprepare belongs to the new generation.
	_, err = sc.Prepare(query)
	require.NoError(t, err)
	state, _ = sc.State(query)
	assert.Equal(t, StmtStatusPrepared, state.Status)
	assert.Equal(t, uint64(1), state.Generation)
	assert.Equal(t, 2, db.PrepareCount)
}

func TestStmtCacheConcurrentSameQueryDeduplicates(t *testing.T) {
	db, drv := newFakeDB(t)
	sc := NewStmtCache(db)
	query := "SELECT 1"
	started, release := drv.gateQuery(query)

	type outcome struct {
		state StmtState
		err   error
	}
	results := make(chan outcome, 2)
	go func() {
		_, state, err := sc.PrepareState(query)
		results <- outcome{state, err}
	}()
	<-started // leader has entered the underlying prepare
	go func() {
		_, state, err := sc.PrepareState(query)
		results <- outcome{state, err}
	}()

	close(release)
	for i := 0; i < 2; i++ {
		res := <-results
		require.NoError(t, res.err)
		assert.Equal(t, uint64(0), res.state.Generation)
	}

	assert.Equal(t, 1, drv.prepareCount(), "concurrent prepares of one query collapse to one")

	// The final observable state after another call is a cache hit.
	_, state, err := sc.PrepareState(query)
	require.NoError(t, err)
	assert.Equal(t, StmtStatusCacheHit, state.Status)
	assert.Equal(t, uint64(0), state.Generation)
	assert.Equal(t, 1, drv.prepareCount())
}

func TestStmtCacheConcurrentClearBumpsGenerationAndFinalizesState(t *testing.T) {
	db, drv := newFakeDB(t)
	sc := NewStmtCache(db)
	query := "SELECT 1"
	started, release := drv.gateQuery(query)

	type outcome struct {
		state StmtState
		err   error
	}
	inflight := make(chan outcome, 1)
	go func() {
		_, state, err := sc.PrepareState(query)
		inflight <- outcome{state, err}
	}()
	<-started

	// Fixed order: Clear runs while the prepare is in flight.
	require.NoError(t, sc.Clear())
	assert.Equal(t, uint64(1), sc.Generation())

	close(release)
	res := <-inflight

	// The stale prepare must not have repopulated the cache.
	assert.Equal(t, 0, len(sc.cache))

	// The orphaned statement produced in the old generation gets closed.
	for _, stmt := range drv.statements(query) {
		assert.True(t, stmt.isClosed(), "stale statement must not leak")
	}

	// A fresh prepare after the boundary lands in generation 1 and succeeds.
	_, state, err := sc.PrepareState(query)
	require.NoError(t, err)
	assert.Equal(t, StmtStatusPrepared, state.Status)
	assert.Equal(t, uint64(1), state.Generation)

	final, ok := sc.State(query)
	require.True(t, ok)
	assert.Equal(t, uint64(1), final.Generation)
	assert.Equal(t, StmtStatusPrepared, final.Status)

	// The in-flight goroutine observed the generation boundary.
	assert.Equal(t, uint64(1), res.state.Generation)
	assert.Error(t, res.err)
}

func TestStmtCacheStateUnknownForUnseenQuery(t *testing.T) {
	db, _ := newFakeDB(t)
	sc := NewStmtCache(db)
	state, ok := sc.State("never seen")
	assert.False(t, ok)
	assert.Equal(t, StmtStatusUnknown, state.Status)
	assert.Equal(t, "unknown", StmtStatusUnknown.String())
	assert.Equal(t, "prepared", StmtStatusPrepared.String())
	assert.Equal(t, "cache_hit", StmtStatusCacheHit.String())
	assert.Equal(t, "failed", StmtStatusFailed.String())
	assert.Equal(t, "canceled", StmtStatusCanceled.String())

	assert.Len(t, sc.States(), 0)
}

func TestStmtCacheConcurrentFailedPrepareWakesFollowersWithoutCaching(t *testing.T) {
	db, drv := newFakeDB(t)
	drv.err = errExplicit
	sc := NewStmtCache(db)
	query := "SELECT fail"
	started, release := drv.gateQuery(query)

	const waiters = 3
	errs := make(chan error, waiters)
	entered := make(chan struct{}, waiters)

	// The first goroutine becomes the in-flight leader.
	go func() {
		_, _, err := sc.PrepareState(query)
		errs <- err
	}()
	<-started

	for i := 0; i < waiters; i++ {
		go func() {
			entered <- struct{}{}
			_, _, err := sc.PrepareState(query)
			errs <- err
		}()
	}
	for i := 0; i < waiters; i++ {
		<-entered
	}
	// Wait until every follower has parked on the in-flight prepare.
	waitFor(t, func() bool { return sc.inflightWaiters(query) == waiters })
	close(release)

	for i := 0; i < waiters; i++ {
		assert.Equal(t, errExplicit, <-errs)
	}

	assert.Equal(t, 1, drv.prepareCount(), "failed prepare still deduplicates concurrent callers")
	assert.NotContains(t, sc.cache, query)
	state, ok := sc.State(query)
	require.True(t, ok)
	assert.Equal(t, StmtStatusFailed, state.Status)
	assert.Equal(t, errExplicit, state.Err)
}

func TestStmtCacheClearWhileInflightFailsFollowers(t *testing.T) {
	db, drv := newFakeDB(t)
	sc := NewStmtCache(db)
	query := "SELECT 1"
	started, release := drv.gateQuery(query)

	type outcome struct {
		state StmtState
		err   error
	}
	leader := make(chan outcome, 1)
	go func() {
		_, state, err := sc.PrepareState(query)
		leader <- outcome{state, err}
	}()
	<-started
	sc.mu.Lock()
	require.Len(t, sc.inflight, 1, "leader must be in flight")
	sc.mu.Unlock()

	follower := make(chan outcome, 1)
	go func() {
		_, state, err := sc.PrepareState(query)
		follower <- outcome{state, err}
	}()
	// Wait until the follower has parked on the leader's in-flight prepare.
	waitFor(t, func() bool { return sc.inflightWaiters(query) == 1 })

	// Fixed order: Clear happens while both goroutines are in flight.
	require.NoError(t, sc.Clear())
	close(release)

	leaderRes := <-leader
	followerRes := <-follower
	assert.Error(t, leaderRes.err)
	assert.Error(t, followerRes.err)
	assert.Equal(t, uint64(1), leaderRes.state.Generation)
	assert.Equal(t, uint64(1), followerRes.state.Generation)
	assert.Equal(t, StmtStatusFailed, leaderRes.state.Status)
	assert.Equal(t, StmtStatusFailed, followerRes.state.Status)

	state, ok := sc.State(query)
	require.True(t, ok)
	assert.Equal(t, uint64(1), state.Generation)
	assert.Equal(t, StmtStatusFailed, state.Status)
}
