package httpapi

import (
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// healthzServer constructs a bare Server suitable for handler-level tests.
// The full New() pipeline requires a live upstream pool and proxy handler;
// neither is exercised by /healthz, so we skip the dependency wiring.
func healthzServer() *Server {
	return &Server{}
}

func TestHealthz_ReturnsOKWhenNotDraining(t *testing.T) {
	s := healthzServer()

	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rr := httptest.NewRecorder()
	s.healthz(rr, req)

	assert.Equal(t, http.StatusOK, rr.Code)
	assert.Equal(t, "ok\n", rr.Body.String())
	assert.Contains(t, rr.Header().Get("Content-Type"), "text/plain")
}

func TestHealthz_Returns503WhenDraining(t *testing.T) {
	s := healthzServer()
	s.draining.Store(true)

	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rr := httptest.NewRecorder()
	s.healthz(rr, req)

	assert.Equal(t, http.StatusServiceUnavailable, rr.Code)
	assert.Equal(t, "draining\n", rr.Body.String())
	assert.Contains(t, rr.Header().Get("Content-Type"), "text/plain")
}

// TestShutdown_SetsDrainingFlag verifies the post-condition that Shutdown
// leaves the flag set. This is a weaker property than ordering — for that
// see TestShutdown_FlipsDrainingDuringDrain. This test only catches the
// regression of forgetting to flip the flag at all.
func TestShutdown_SetsDrainingFlag(t *testing.T) {
	s := &Server{srv: &http.Server{}}

	require.False(t, s.draining.Load(), "draining must start false")
	// http.Server.Shutdown on a never-started server returns nil immediately,
	// which lets us inspect post-call state without spinning up a listener.
	require.NoError(t, s.Shutdown(0))
	require.True(t, s.draining.Load(), "Shutdown must flip draining to true")

	// Subsequent /healthz probes now see the flag and return 503.
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rr := httptest.NewRecorder()
	s.healthz(rr, req)
	assert.Equal(t, http.StatusServiceUnavailable, rr.Code)
}

// TestShutdown_FlipsDrainingDuringDrain verifies the ordering invariant
// that draining is set BEFORE srv.Shutdown begins waiting for in-flight
// requests, not after. To catch this, we hold an in-flight request open
// so srv.Shutdown blocks mid-drain, then assert the flag is already true.
//
// The regression this guards against:
//
//	func (s *Server) Shutdown(...) error {
//	    err := s.srv.Shutdown(ctx)   // blocks here while draining
//	    s.draining.Store(true)       // ← only flips after — too late
//	    return err
//	}
//
// In that broken implementation, an LB probe arriving on an existing
// keepalive connection during the drain window would still see 200,
// keep dispatching traffic, and trip exactly the bug this PR fixes.
func TestShutdown_FlipsDrainingDuringDrain(t *testing.T) {
	s := &Server{}

	// slowHandler holds an in-flight request open so srv.Shutdown blocks
	// long enough for us to observe the draining state mid-drain.
	requestStarted := make(chan struct{})
	requestRelease := make(chan struct{})
	slowHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(requestStarted)
		<-requestRelease
		w.WriteHeader(http.StatusOK)
	})

	mux := http.NewServeMux()
	mux.Handle("/slow", slowHandler)
	mux.HandleFunc("/healthz", s.healthz)
	s.srv = &http.Server{Handler: mux}

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)

	serverDone := make(chan error, 1)
	go func() { serverDone <- s.srv.Serve(listener) }()

	addr := listener.Addr().String()

	// Send a request that will block in slowHandler.
	clientDone := make(chan error, 1)
	go func() {
		resp, err := http.Get(fmt.Sprintf("http://%s/slow", addr))
		if err == nil {
			resp.Body.Close()
		}
		clientDone <- err
	}()
	<-requestStarted // wait until slowHandler is actually running

	// Now invoke Shutdown — it will block on the in-flight /slow request.
	shutdownDone := make(chan error, 1)
	go func() { shutdownDone <- s.Shutdown(2 * time.Second) }()

	// Give Shutdown a moment to enter and (correctly) flip the flag.
	// 50ms is well above the ~µs cost of an atomic.Store and well below
	// the test's 2s timeout, so this is reliable on any reasonable runner.
	time.Sleep(50 * time.Millisecond)

	// Core assertion: draining must already be true while srv.Shutdown
	// is still blocked draining the in-flight /slow request.
	assert.True(t, s.draining.Load(),
		"draining flag must be set before srv.Shutdown begins waiting on in-flight requests")

	// Cleanup: release the slow handler so Shutdown can complete.
	close(requestRelease)
	require.NoError(t, <-shutdownDone)
	require.NoError(t, <-clientDone)
	<-serverDone // Serve returns http.ErrServerClosed after Shutdown
}

// TestNew_WiresHealthzRoute is a smoke test that verifies New()'s route
// table actually dispatches /healthz to s.healthz, not some other handler.
// The handler-level tests above bypass New() and call s.healthz directly,
// so a regression where New() routed /healthz to (say) the / fallback
// would slip past them.
func TestNew_WiresHealthzRoute(t *testing.T) {
	// proxy.Handler and upstream.Pool are not exercised by /healthz; pass
	// nil pointers — the route closures for / and /ws dereference them
	// only when their own routes are hit, which this test does not do.
	s := New(8080, nil, nil)

	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rr := httptest.NewRecorder()
	s.srv.Handler.ServeHTTP(rr, req)

	assert.Equal(t, http.StatusOK, rr.Code, "/healthz must return 200 when not draining")
	assert.Equal(t, "ok\n", rr.Body.String())

	// Flip the flag and verify the SAME route now returns 503. If New()
	// had wired /healthz to a different handler, the response would be
	// unchanged when we mutate s.draining.
	s.draining.Store(true)
	rr = httptest.NewRecorder()
	s.srv.Handler.ServeHTTP(rr, req)

	assert.Equal(t, http.StatusServiceUnavailable, rr.Code,
		"flipping s.draining must be observable through the New()-built mux, proving the route is wired to s.healthz")
}
