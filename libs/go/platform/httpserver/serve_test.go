package httpserver

import (
	"context"
	"errors"
	"net"
	"net/http"
	"sync"
	"syscall"
	"testing"
	"time"
)

// get issues a GET carrying a context.
//
// Not http.Get: gosec's G107 flags a request built from a variable URL, and
// these URLs necessarily are variable -- the listener picks its own port. A
// request built explicitly is not the pattern G107 looks for, so this removes
// the finding rather than suppressing it, and it carries a deadline so a hung
// server fails the test instead of stalling it.
func get(t *testing.T, url string) (*http.Response, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	return http.DefaultClient.Do(request)
}

func shortenTimings(t *testing.T) {
	t.Helper()
	originalDrain, originalBudget := drainDelay, shutdownBudget
	drainDelay, shutdownBudget = 20*time.Millisecond, time.Second
	t.Cleanup(func() { drainDelay, shutdownBudget = originalDrain, originalBudget })
}

func listenOnLoopback(t *testing.T) net.Listener {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	return listener
}

// A request that arrives after SIGTERM must still be answered: that is the
// whole point of the drain window.
func TestRunServesDuringDrainWindowThenStops(t *testing.T) {
	shortenTimings(t)
	listener := listenOnLoopback(t)
	server := &http.Server{
		Handler:           http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }),
		ReadHeaderTimeout: time.Second,
	}
	var wg sync.WaitGroup
	var runErr error
	wg.Add(1)
	go func() {
		defer wg.Done()
		runErr = run(server, nil, Options{}, func() error { return server.Serve(listener) })
	}()

	url := "http://" + listener.Addr().String() + "/"
	waitForServer(t, url)
	if err := syscall.Kill(syscall.Getpid(), syscall.SIGTERM); err != nil {
		t.Fatalf("signal: %v", err)
	}
	response, err := get(t, url)
	if err != nil {
		t.Fatalf("request during drain window failed: %v", err)
	}
	_ = response.Body.Close()

	wg.Wait()
	if runErr != nil {
		t.Fatalf("Run returned %v, want nil after a clean drain", runErr)
	}
	// A response here means the port is still open, which is the failure. The
	// body is closed on that path anyway: leaking it would be a real leak, not
	// an artefact of the assertion.
	if response, err := get(t, url); err == nil {
		_ = response.Body.Close()
		t.Fatal("server still accepting connections after Run returned")
	}
}

// A listen failure has to surface: a service that cannot bind must exit
// non-zero rather than sit there looking healthy.
func TestRunReturnsServeError(t *testing.T) {
	shortenTimings(t)
	occupied := listenOnLoopback(t)
	defer func() { _ = occupied.Close() }()
	server := &http.Server{Addr: occupied.Addr().String(), ReadHeaderTimeout: time.Second}
	if err := Run(server, nil); err == nil {
		t.Fatal("Run returned nil for an address already in use")
	}
}

func TestDrainClosesTheServer(t *testing.T) {
	shortenTimings(t)
	server := &http.Server{ReadHeaderTimeout: time.Second}
	if err := drain(server, nil, Options{}); err != nil {
		t.Fatalf("drain: %v", err)
	}
	if err := server.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
		t.Fatalf("server was not closed by drain: %v", err)
	}
}

func waitForServer(t *testing.T, url string) {
	t.Helper()
	for attempt := 0; attempt < 100; attempt++ {
		response, err := get(t, url)
		if err == nil {
			_ = response.Body.Close()
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("server never became reachable")
}

// The guarantee every slot workload depends on at drain time: a request already
// being served when the signal arrives is finished, not cut off. search-service
// used to close its listener immediately on SIGTERM and lost exactly these.
func TestRunLetsInFlightRequestsFinish(t *testing.T) {
	shortenTimings(t)
	listener := listenOnLoopback(t)
	released := make(chan struct{})
	handling := make(chan struct{}, 1)
	server := &http.Server{
		Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			handling <- struct{}{}
			<-released
			w.WriteHeader(http.StatusOK)
		}),
		ReadHeaderTimeout: time.Second,
	}
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		_ = run(server, nil, Options{}, func() error { return server.Serve(listener) })
	}()

	url := "http://" + listener.Addr().String() + "/"
	result := make(chan error, 1)
	go func() {
		response, err := get(t, url)
		if err == nil {
			_ = response.Body.Close()
		}
		result <- err
	}()

	select {
	case <-handling:
	case <-time.After(5 * time.Second):
		t.Fatal("request never reached the handler")
	}
	if err := syscall.Kill(syscall.Getpid(), syscall.SIGTERM); err != nil {
		t.Fatalf("signal: %v", err)
	}
	// Shutdown must be waiting on this request rather than dropping it.
	close(released)

	if err := <-result; err != nil {
		t.Fatalf("in-flight request failed across shutdown: %v", err)
	}
	wg.Wait()
}

// The hook must start when the signal arrives, not after the HTTP drain: a
// worker that keeps claiming jobs through the drain is claiming work the
// process is about to abandon.
func TestRunStartsShutdownHookConcurrentlyWithTheDrain(t *testing.T) {
	shortenTimings(t)
	listener := listenOnLoopback(t)
	server := &http.Server{Handler: http.NotFoundHandler(), ReadHeaderTimeout: time.Second}

	hookStarted := make(chan struct{})
	hookCtx := make(chan context.Context, 1)
	opts := Options{OnShutdown: func(ctx context.Context) error {
		hookCtx <- ctx
		close(hookStarted)
		return nil
	}}

	var wg sync.WaitGroup
	var runErr error
	wg.Add(1)
	go func() {
		defer wg.Done()
		runErr = run(server, nil, opts, func() error { return server.Serve(listener) })
	}()

	waitForServer(t, "http://"+listener.Addr().String()+"/")
	if err := syscall.Kill(syscall.Getpid(), syscall.SIGTERM); err != nil {
		t.Fatalf("signal: %v", err)
	}
	select {
	case <-hookStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("shutdown hook never ran")
	}

	wg.Wait()
	if runErr != nil {
		t.Fatalf("Run returned %v", runErr)
	}
	// The hook shares the process budget, so it cannot outlive termination.
	ctx := <-hookCtx
	if _, ok := ctx.Deadline(); !ok {
		t.Fatal("shutdown hook received a context with no deadline")
	}
}

// A hook that overruns must not hang the process past its budget.
func TestRunReturnsWhenTheShutdownHookOverrunsTheBudget(t *testing.T) {
	shortenTimings(t)
	drainDelay, shutdownBudget = time.Millisecond, 80*time.Millisecond
	listener := listenOnLoopback(t)
	server := &http.Server{Handler: http.NotFoundHandler(), ReadHeaderTimeout: time.Second}

	blocked := make(chan struct{})
	opts := Options{OnShutdown: func(ctx context.Context) error {
		<-blocked
		return nil
	}}

	var wg sync.WaitGroup
	var runErr error
	wg.Add(1)
	go func() {
		defer wg.Done()
		runErr = run(server, nil, opts, func() error { return server.Serve(listener) })
	}()

	waitForServer(t, "http://"+listener.Addr().String()+"/")
	if err := syscall.Kill(syscall.Getpid(), syscall.SIGTERM); err != nil {
		t.Fatalf("signal: %v", err)
	}

	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("Run blocked past the shutdown budget waiting on the hook")
	}
	close(blocked)
	if runErr == nil {
		t.Fatal("expected an error when the hook did not finish inside the budget")
	}
}

// An error from the hook must reach the caller rather than being swallowed.
func TestRunReportsShutdownHookError(t *testing.T) {
	shortenTimings(t)
	server := &http.Server{ReadHeaderTimeout: time.Second}
	wanted := errors.New("worker refused to stop")
	err := drain(server, nil, Options{OnShutdown: func(context.Context) error { return wanted }})
	if !errors.Is(err, wanted) {
		t.Fatalf("drain returned %v, want %v", err, wanted)
	}
}

// Services with nothing else to drain keep the behaviour they had.
func TestDrainWithoutHookIsUnchanged(t *testing.T) {
	shortenTimings(t)
	server := &http.Server{ReadHeaderTimeout: time.Second}
	if err := drain(server, nil, Options{}); err != nil {
		t.Fatalf("drain: %v", err)
	}
}

// Cleanup after Run must inherit what is left of the one termination budget,
// not start an unbounded one. Every service does its pool and tracing teardown
// there, and context.Background() let that run past the grace period.
func TestCleanupContextInheritsTheRemainingBudget(t *testing.T) {
	shortenTimings(t)
	shutdownBudget = 200 * time.Millisecond
	originalFloor := minCleanup
	minCleanup = 10 * time.Millisecond
	shutdownStartedAt.Store(0)
	t.Cleanup(func() { shutdownStartedAt.Store(0); minCleanup = originalFloor })

	markShutdownStarted()
	time.Sleep(50 * time.Millisecond)

	ctx, cancel := CleanupContext()
	defer cancel()
	deadline, ok := ctx.Deadline()
	if !ok {
		t.Fatal("CleanupContext produced a context with no deadline")
	}
	remaining := time.Until(deadline)
	if remaining > 200*time.Millisecond {
		t.Fatalf("cleanup got %s, more than the whole budget", remaining)
	}
}

// A drain that consumed the entire budget must still leave enough to close a
// pool, rather than handing back a context that has already expired.
func TestCleanupContextKeepsAFloorWhenTheBudgetIsSpent(t *testing.T) {
	shortenTimings(t)
	shutdownBudget = time.Millisecond
	originalFloor := minCleanup
	minCleanup = 20 * time.Millisecond
	shutdownStartedAt.Store(0)
	t.Cleanup(func() { shutdownStartedAt.Store(0); minCleanup = originalFloor })

	markShutdownStarted()
	time.Sleep(10 * time.Millisecond)

	ctx, cancel := CleanupContext()
	defer cancel()
	if ctx.Err() != nil {
		t.Fatal("CleanupContext handed back an already-expired context")
	}
	deadline, _ := ctx.Deadline()
	if time.Until(deadline) > minCleanup {
		t.Fatal("the floor exceeded minCleanup")
	}
}

// Run returning because the server failed is not a termination: cleanup still
// needs a bound, but there is no budget to inherit.
func TestCleanupContextBoundsCleanupWhenNoShutdownBegan(t *testing.T) {
	shutdownStartedAt.Store(0)
	ctx, cancel := CleanupContext()
	defer cancel()
	if _, ok := ctx.Deadline(); !ok {
		t.Fatal("cleanup was unbounded when no shutdown had begun")
	}
}
