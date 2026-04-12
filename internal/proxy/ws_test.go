package proxy

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ChickenBenny/AegisRPC/internal/upstream"
	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ─── helpers ─────────────────────────────────────────────────────────────────

// mockUpstreamWS starts a WebSocket server that runs handler for each connection.
// The server URL has the "ws" scheme already substituted.
func mockUpstreamWS(t *testing.T, handler func(*websocket.Conn)) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		handler(conn)
	}))
	t.Cleanup(srv.Close)
	return "ws" + strings.TrimPrefix(srv.URL, "http")
}

// dialTestServer connects a WebSocket client to the given httptest.Server.
func dialTestServer(t *testing.T, srv *httptest.Server) *websocket.Conn {
	t.Helper()
	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http")
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	require.NoError(t, err)
	t.Cleanup(func() { conn.Close() })
	return conn
}

// newTestSession creates a wsSession without a real client conn — for pure-logic unit tests.
func newTestSession() *wsSession {
	return &wsSession{
		subs:       make(map[string]*subscription),
		upToClient: make(map[string]string),
		pending:    make(map[string]json.RawMessage),
	}
}

// ─── httpToWS ────────────────────────────────────────────────────────────────

func TestHTTPToWS(t *testing.T) {
	cases := []struct{ in, want string }{
		{"http://localhost:8546", "ws://localhost:8546"},
		{"https://eth.llamarpc.com", "wss://eth.llamarpc.com"},
		{"ws://already-ws", "ws://already-ws"},
	}
	for _, tc := range cases {
		assert.Equal(t, tc.want, httpToWS(tc.in), tc.in)
	}
}

// ─── rewriteUnsubscribe ──────────────────────────────────────────────────────

func TestWSSession_RewriteUnsubscribe_RemapsID(t *testing.T) {
	sess := newTestSession()
	// Pre-populate: client knows "0xCLIENT", upstream currently uses "0xUP"
	sess.subs["0xCLIENT"] = &subscription{
		clientID: "0xCLIENT", upstreamID: "0xUP",
	}
	sess.upToClient["0xUP"] = "0xCLIENT"

	msg := []byte(`{"jsonrpc":"2.0","id":9,"method":"eth_unsubscribe","params":["0xCLIENT"]}`)
	out := sess.rewriteUnsubscribe(msg)

	var req struct {
		Params []string `json:"params"`
	}
	require.NoError(t, json.Unmarshal(out, &req))
	assert.Equal(t, "0xUP", req.Params[0], "unsubscribe must use the current upstream ID")

	// Subscription must be removed from state.
	assert.Empty(t, sess.subs)
	assert.Empty(t, sess.upToClient)
}

func TestWSSession_RewriteUnsubscribe_UnknownID_Passthrough(t *testing.T) {
	sess := newTestSession()
	msg := []byte(`{"jsonrpc":"2.0","id":1,"method":"eth_unsubscribe","params":["0xUNKNOWN"]}`)
	out := sess.rewriteUnsubscribe(msg)
	assert.Equal(t, msg, out, "unknown sub ID must pass through unchanged")
}

func TestWSSession_RewriteUnsubscribe_IDsMatch_Passthrough(t *testing.T) {
	sess := newTestSession()
	// clientID == upstreamID (no failover has happened yet)
	sess.subs["0xSAME"] = &subscription{clientID: "0xSAME", upstreamID: "0xSAME"}
	sess.upToClient["0xSAME"] = "0xSAME"

	msg := []byte(`{"jsonrpc":"2.0","id":1,"method":"eth_unsubscribe","params":["0xSAME"]}`)
	out := sess.rewriteUnsubscribe(msg)
	assert.Equal(t, msg, out, "when IDs match no rewrite is needed")
}

// ─── replaySubscriptions ─────────────────────────────────────────────────────

func TestWSSession_ReplaySubscriptions_UpdatesMapping(t *testing.T) {
	upstreamURL := mockUpstreamWS(t, func(conn *websocket.Conn) {
		_, msg, err := conn.ReadMessage()
		require.NoError(t, err)

		var req map[string]any
		require.NoError(t, json.Unmarshal(msg, &req))
		assert.Equal(t, "eth_subscribe", req["method"])

		// Respond with a NEW upstream sub ID (simulating a different node)
		conn.WriteJSON(map[string]any{
			"jsonrpc": "2.0",
			"id":      req["id"],
			"result":  "0xNEWUPSTREAMID",
		})
	})

	sess := newTestSession()
	// Existing subscription: client has "0xCLIENTID", old upstream gave "0xOLDID"
	sess.subs["0xCLIENTID"] = &subscription{
		subscribeParams: json.RawMessage(`["newHeads"]`),
		clientID:        "0xCLIENTID",
		upstreamID:      "0xOLDID",
	}
	sess.upToClient["0xOLDID"] = "0xCLIENTID"

	upConn, _, err := websocket.DefaultDialer.Dial(upstreamURL, nil)
	require.NoError(t, err)
	defer upConn.Close()

	require.NoError(t, sess.replaySubscriptions(upConn))

	// upstreamID must be updated to the new ID
	assert.Equal(t, "0xNEWUPSTREAMID", sess.subs["0xCLIENTID"].upstreamID)
	// New upstream ID must map to the stable client ID
	assert.Equal(t, "0xCLIENTID", sess.upToClient["0xNEWUPSTREAMID"])
	// Old upstream ID must be removed
	_, oldStillPresent := sess.upToClient["0xOLDID"]
	assert.False(t, oldStillPresent, "stale upstream ID must be removed from upToClient")
}

func TestWSSession_ReplaySubscriptions_Empty_NoOp(t *testing.T) {
	// No subscriptions → replay should succeed without touching the upstream.
	upstreamURL := mockUpstreamWS(t, func(conn *websocket.Conn) {
		// Should not receive anything.
		conn.SetReadDeadline(time.Now().Add(100 * time.Millisecond))
		_, _, err := conn.ReadMessage()
		assert.Error(t, err, "upstream should not receive any message when there are no subscriptions")
	})

	sess := newTestSession()

	upConn, _, err := websocket.DefaultDialer.Dial(upstreamURL, nil)
	require.NoError(t, err)
	defer upConn.Close()

	assert.NoError(t, sess.replaySubscriptions(upConn))
}

// ─── ServeWS integration ─────────────────────────────────────────────────────

// TestServeWS_ProxiesMessages verifies that arbitrary JSON-RPC messages are
// forwarded in both directions through the virtual WS session.
func TestServeWS_ProxiesMessages(t *testing.T) {
	upstreamReceived := make(chan []byte, 1)
	upstreamURL := mockUpstreamWS(t, func(conn *websocket.Conn) {
		_, msg, err := conn.ReadMessage()
		if err != nil {
			return
		}
		upstreamReceived <- msg
		// Echo a response back.
		conn.WriteMessage(websocket.TextMessage, []byte(`{"jsonrpc":"2.0","id":1,"result":"0x1"}`))
		// Keep the connection open until test finishes.
		conn.SetReadDeadline(time.Now().Add(2 * time.Second))
		conn.ReadMessage()
	})

	pool := poolWithURL(t, upstreamURL)
	srv := httptest.NewServer(ServeWS(pool))
	t.Cleanup(srv.Close)

	client := dialTestServer(t, srv)

	req := `{"jsonrpc":"2.0","id":1,"method":"eth_blockNumber","params":[]}`
	require.NoError(t, client.WriteMessage(websocket.TextMessage, []byte(req)))

	// Upstream should receive the message.
	select {
	case msg := <-upstreamReceived:
		assert.Contains(t, string(msg), "eth_blockNumber")
	case <-time.After(2 * time.Second):
		t.Fatal("timeout: upstream did not receive the message")
	}

	// Client should receive the response.
	client.SetReadDeadline(time.Now().Add(2 * time.Second))
	_, resp, err := client.ReadMessage()
	require.NoError(t, err)
	assert.Contains(t, string(resp), `"result":"0x1"`)
}

// TestServeWS_Subscribe_RecordsAndForwardsNotification exercises the full
// subscribe → notification path:
//  1. Client sends eth_subscribe.
//  2. Upstream responds with a subscription ID.
//  3. Upstream sends an eth_subscription notification.
//  4. Client receives both the response and the notification unchanged.
func TestServeWS_Subscribe_RecordsAndForwardsNotification(t *testing.T) {
	const subID = "0xcd0c3e8af590364f"

	upstreamURL := mockUpstreamWS(t, func(conn *websocket.Conn) {
		// Step 1: read subscribe request
		_, msg, err := conn.ReadMessage()
		require.NoError(t, err)
		var req map[string]any
		require.NoError(t, json.Unmarshal(msg, &req))

		// Step 2: respond with sub ID
		conn.WriteJSON(map[string]any{
			"jsonrpc": "2.0",
			"id":      req["id"],
			"result":  subID,
		})

		// Step 3: push a notification
		conn.WriteJSON(map[string]any{
			"jsonrpc": "2.0",
			"method":  "eth_subscription",
			"params": map[string]any{
				"subscription": subID,
				"result":       map[string]any{"number": "0x10"},
			},
		})

		conn.SetReadDeadline(time.Now().Add(2 * time.Second))
		conn.ReadMessage()
	})

	pool := poolWithURL(t, upstreamURL)
	srv := httptest.NewServer(ServeWS(pool))
	t.Cleanup(srv.Close)

	client := dialTestServer(t, srv)
	client.SetReadDeadline(time.Now().Add(3 * time.Second))

	subReq := `{"jsonrpc":"2.0","id":1,"method":"eth_subscribe","params":["newHeads"]}`
	require.NoError(t, client.WriteMessage(websocket.TextMessage, []byte(subReq)))

	// Receive subscribe response
	_, resp, err := client.ReadMessage()
	require.NoError(t, err)
	assert.Contains(t, string(resp), subID, "subscribe response must contain the sub ID")

	// Receive notification — ID must be unchanged (no failover has happened)
	_, notif, err := client.ReadMessage()
	require.NoError(t, err)
	assert.Contains(t, string(notif), subID, "notification must carry the client's subscription ID")
	assert.Contains(t, string(notif), "eth_subscription")
}

// TestServeWS_Failover_RemapsSubscriptionID verifies the core failover guarantee:
// after the upstream drops, AegisRPC reconnects, replays the subscription, and
// rewrites the new upstream subscription ID back to the original client ID.
func TestServeWS_Failover_RemapsSubscriptionID(t *testing.T) {
	const clientSubID = "0xAAAA"
	const newUpstreamSubID = "0xBBBB"

	// upstream1: accepts subscribe, sends one notification, then closes.
	var upstream1Once sync.Once
	upstream1Done := make(chan struct{})
	upstream1 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		upstream1Once.Do(func() {
			// Read subscribe
			_, msg, _ := conn.ReadMessage()
			var req map[string]any
			json.Unmarshal(msg, &req)

			// Respond with clientSubID (first time: clientID == upstreamID)
			conn.WriteJSON(map[string]any{
				"jsonrpc": "2.0", "id": req["id"], "result": clientSubID,
			})
			// Push one notification
			conn.WriteJSON(map[string]any{
				"jsonrpc": "2.0",
				"method":  "eth_subscription",
				"params":  map[string]any{"subscription": clientSubID, "result": "first"},
			})
			close(upstream1Done)
			// Close to trigger failover
		})
	}))
	t.Cleanup(upstream1.Close)

	// upstream2: accepts the replay subscribe, assigns a NEW sub ID, sends a notification.
	notifReceived := make(chan string, 1)
	upstream2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()

		// Read replay subscribe
		_, msg, err := conn.ReadMessage()
		if err != nil {
			return
		}
		var req map[string]any
		json.Unmarshal(msg, &req)

		// Respond with a DIFFERENT sub ID
		conn.WriteJSON(map[string]any{
			"jsonrpc": "2.0", "id": req["id"], "result": newUpstreamSubID,
		})
		// Push notification with the new ID
		conn.WriteJSON(map[string]any{
			"jsonrpc": "2.0",
			"method":  "eth_subscription",
			"params":  map[string]any{"subscription": newUpstreamSubID, "result": "second"},
		})
		conn.SetReadDeadline(time.Now().Add(3 * time.Second))
		conn.ReadMessage()
	}))
	t.Cleanup(upstream2.Close)

	// Build a pool that alternates: upstream1 first, upstream2 second.
	ws1 := "ws" + strings.TrimPrefix(upstream1.URL, "http")
	ws2 := "ws" + strings.TrimPrefix(upstream2.URL, "http")
	pool := poolWithURLs(t, ws1, ws2)

	srv := httptest.NewServer(ServeWS(pool))
	t.Cleanup(srv.Close)

	client := dialTestServer(t, srv)
	client.SetReadDeadline(time.Now().Add(5 * time.Second))

	// Subscribe
	subReq := `{"jsonrpc":"2.0","id":1,"method":"eth_subscribe","params":["newHeads"]}`
	require.NoError(t, client.WriteMessage(websocket.TextMessage, []byte(subReq)))

	// Read subscribe response
	_, resp, err := client.ReadMessage()
	require.NoError(t, err)
	assert.Contains(t, string(resp), clientSubID)

	// Read first notification (from upstream1, unchanged)
	_, notif1, err := client.ReadMessage()
	require.NoError(t, err)
	assert.Contains(t, string(notif1), clientSubID, "first notification: must use client sub ID")

	// Wait for upstream1 to close (triggers reconnect)
	select {
	case <-upstream1Done:
	case <-time.After(3 * time.Second):
		t.Fatal("upstream1 did not close in time")
	}

	// Read second notification (from upstream2, remapped from 0xBBBB → 0xAAAA)
	client.SetReadDeadline(time.Now().Add(5 * time.Second))
	_, notif2, err := client.ReadMessage()
	require.NoError(t, err, "client should receive second notification after failover")

	// The client must see clientSubID, not the new upstream ID.
	notifReceived <- string(notif2)
	got := <-notifReceived
	assert.Contains(t, got, clientSubID,
		"post-failover notification must be remapped to the original client sub ID")
	assert.NotContains(t, got, newUpstreamSubID,
		"new upstream sub ID must not leak to the client")
}

// ─── pool helpers ────────────────────────────────────────────────────────────

// poolWithURL builds a Pool pointed at a single WS URL.
// The WS URL is stored as http:// so upstream.NewUpstream can parse it, then
// connectUpstream converts it back via httpToWS.
func poolWithURL(t *testing.T, wsURL string) *upstream.Pool {
	t.Helper()
	httpURL := "http" + strings.TrimPrefix(wsURL, "ws")
	pool, err := upstream.NewPool([]string{httpURL})
	require.NoError(t, err)
	pool.Nodes()[0].SetHealthy(true)
	return pool
}

// poolWithURLs builds a Pool pointed at two WS URLs.
func poolWithURLs(t *testing.T, wsURL1, wsURL2 string) *upstream.Pool {
	t.Helper()
	http1 := "http" + strings.TrimPrefix(wsURL1, "ws")
	http2 := "http" + strings.TrimPrefix(wsURL2, "ws")
	pool, err := upstream.NewPool([]string{http1, http2})
	require.NoError(t, err)
	for _, n := range pool.Nodes() {
		n.SetHealthy(true)
	}
	return pool
}

// ─── context cancel ──────────────────────────────────────────────────────────

func TestServeWS_ContextCancel_ClosesSession(t *testing.T) {
	upstreamURL := mockUpstreamWS(t, func(conn *websocket.Conn) {
		conn.SetReadDeadline(time.Now().Add(2 * time.Second))
		conn.ReadMessage()
	})

	// Create a loopback WS pair to use as the client connection.
	clientConnCh := make(chan *websocket.Conn, 1)
	clientSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		clientConnCh <- conn
		conn.SetReadDeadline(time.Now().Add(3 * time.Second))
		conn.ReadMessage()
	}))
	t.Cleanup(clientSrv.Close)

	// Dial to get the "client" side of the pair (what the session uses).
	clientWS := "ws" + strings.TrimPrefix(clientSrv.URL, "http")
	_, _, err := websocket.DefaultDialer.Dial(clientWS, nil)
	require.NoError(t, err)
	serverSideConn := <-clientConnCh

	pool := poolWithURL(t, upstreamURL)
	ctx, cancel := context.WithCancel(context.Background())

	sess := &wsSession{
		pool:       pool,
		client:     serverSideConn, // the session's "client" conn
		subs:       make(map[string]*subscription),
		upToClient: make(map[string]string),
		pending:    make(map[string]json.RawMessage),
	}

	upConn, _, err := websocket.DefaultDialer.Dial(upstreamURL, nil)
	require.NoError(t, err)

	done := make(chan bool, 1)
	go func() {
		done <- sess.pump(ctx, upConn)
	}()

	cancel()

	select {
	case clientLeft := <-done:
		assert.True(t, clientLeft, "context cancel should be treated as client leaving")
	case <-time.After(2 * time.Second):
		t.Fatal("pump did not exit after context cancel")
	}
}
