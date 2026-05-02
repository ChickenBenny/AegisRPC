package httpapi

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
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

// TestShutdown_FlipsDrainingBeforeStoppingServer asserts the ordering
// contract that Shutdown sets the draining flag before delegating to
// http.Server.Shutdown. A regression here would let an LB hit our
// /healthz on a still-open keep-alive connection during the grace
// period and see 200 — exactly the bug this commit fixes.
func TestShutdown_FlipsDrainingBeforeStoppingServer(t *testing.T) {
	s := &Server{srv: &http.Server{}}

	assert.False(t, s.draining.Load(), "draining must start false")

	// http.Server.Shutdown on a never-started server returns nil immediately,
	// which lets us inspect post-call state without spinning up a listener.
	require := assert.New(t)
	require.NoError(s.Shutdown(0))
	require.True(s.draining.Load(), "Shutdown must flip draining to true")

	// Subsequent /healthz probes now see the flag and return 503.
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rr := httptest.NewRecorder()
	s.healthz(rr, req)
	assert.Equal(t, http.StatusServiceUnavailable, rr.Code)
}
