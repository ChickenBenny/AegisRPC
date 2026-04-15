package proxy

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/ChickenBenny/AegisRPC/internal/upstream"
	"github.com/gorilla/websocket"
)

var upgrader = websocket.Upgrader{
	// Allow all origins in the proxy; callers are responsible for auth.
	CheckOrigin: func(r *http.Request) bool { return true },
}

// subscription tracks one active eth_subscribe registration.
type subscription struct {
	subscribeParams json.RawMessage // params to replay on reconnect (e.g. ["newHeads"])
	clientID        string          // subscription ID the client was given (stable across failovers)
	upstreamID      string          // subscription ID the current upstream assigned (changes on failover)
}

// wsSession manages one virtual WebSocket session between a single client and the upstream pool.
// The client's connection is kept alive even when the upstream drops; subscriptions are replayed
// on the new upstream and their IDs are remapped transparently.
type wsSession struct {
	pool   *upstream.Pool
	client *websocket.Conn

	mu         sync.Mutex
	subs       map[string]*subscription  // clientID → subscription
	upToClient map[string]string         // upstreamID → clientID (updated on failover)
	pending    map[string]json.RawMessage // raw request ID → subscribe params (awaiting response)
}

// clientFrame is one WebSocket frame received from the client.
type clientFrame struct {
	mt  int
	msg []byte
}

func newWSSession(pool *upstream.Pool, client *websocket.Conn) *wsSession {
	return &wsSession{
		pool:       pool,
		client:     client,
		subs:       make(map[string]*subscription),
		upToClient: make(map[string]string),
		pending:    make(map[string]json.RawMessage),
	}
}

// clientWriteTimeout is the maximum time allowed for a single write to the client.
// If the client's TCP window is full for longer than this, the session is terminated
// so a stalled client cannot block the upstream reader goroutine indefinitely.
const clientWriteTimeout = 10 * time.Second

// maxPendingSubscribes caps the number of in-flight eth_subscribe requests
// per session. A misbehaving or unresponsive upstream could otherwise leave
// entries in s.pending forever while the client keeps issuing new subscribes,
// growing the map without bound for the lifetime of the session.
const maxPendingSubscribes = 1024

// writeToClient sends a WebSocket frame to the client with a write deadline.
func (s *wsSession) writeToClient(mt int, msg []byte) error {
	s.client.SetWriteDeadline(time.Now().Add(clientWriteTimeout))
	return s.client.WriteMessage(mt, msg)
}

// ServeWS returns an http.HandlerFunc that upgrades the connection to WebSocket
// and runs a virtual session backed by the upstream pool.
func ServeWS(pool *upstream.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			log.Printf("[ws] upgrade: %v", err)
			return
		}
		defer conn.Close()
		newWSSession(pool, conn).run(r.Context())
	}
}

// run is the session main loop. It connects to an upstream, pumps messages bidirectionally,
// and reconnects transparently whenever the upstream drops.
//
// A single goroutine owns s.client reads for the entire session lifetime, ensuring
// gorilla/websocket's "one concurrent reader" contract is never violated across reconnects.
func (s *wsSession) run(ctx context.Context) {
	// fromClient carries frames from the client to the current upstream.
	// clientGone is closed when the client disconnects.
	fromClient := make(chan clientFrame, 8)
	clientGone := make(chan struct{})
	go func() {
		defer close(clientGone)
		for {
			mt, msg, err := s.client.ReadMessage()
			if err != nil {
				return
			}
			select {
			case fromClient <- clientFrame{mt, msg}:
			case <-ctx.Done():
				return
			}
		}
	}()

	for {
		up, err := s.connectUpstream()
		if err != nil {
			log.Printf("[ws] no upstream available: %v", err)
			_ = s.writeToClient(websocket.CloseMessage,
				websocket.FormatCloseMessage(websocket.CloseInternalServerErr, "no upstream"))
			return
		}

		if err := s.replaySubscriptions(up); err != nil {
			log.Printf("[ws] replay failed: %v", err)
			up.Close()
			continue
		}

		clientLeft := s.pump(ctx, up, fromClient, clientGone)
		up.Close()

		if clientLeft {
			return // client closed — end the session
		}
		log.Printf("[ws] upstream dropped, reconnecting")
	}
}

// connectUpstream dials the next healthy upstream node via WebSocket.
// It tries every node in the pool before giving up, so a transient dial
// failure on one node does not terminate the session.
func (s *wsSession) connectUpstream() (*websocket.Conn, error) {
	n := len(s.pool.Nodes())
	for i := 0; i < n; i++ {
		node := s.pool.Next()
		if node == nil {
			break
		}
		wsURL := httpToWS(node.URL.String())
		header := http.Header{"Origin": {"http://localhost"}}
		conn, _, err := websocket.DefaultDialer.Dial(wsURL, header)
		if err != nil {
			log.Printf("[ws] dial %s failed: %v, trying next", wsURL, err)
			continue
		}
		return conn, nil
	}
	return nil, fmt.Errorf("no reachable upstream")
}

// replaySubscriptions re-sends every known subscription to a fresh upstream connection
// and updates the upstreamID mapping so that future notifications are remapped correctly.
//
// All subscribe requests are sent first, then responses are collected using a pending map.
// This tolerates upstreams that push early eth_subscription notifications before the
// subscribe response arrives (e.g. Erigon), and correctly handles out-of-order responses.
func (s *wsSession) replaySubscriptions(up *websocket.Conn) error {
	s.mu.Lock()
	subs := make([]*subscription, 0, len(s.subs))
	for _, sub := range s.subs {
		subs = append(subs, sub)
	}
	s.mu.Unlock()

	if len(subs) == 0 {
		return nil
	}

	// pendingReplay maps each replay request ID to its subscription so responses
	// can be matched out-of-order.
	pendingReplay := make(map[string]*subscription, len(subs))

	// Phase 1: send all subscribe requests at once.
	for _, sub := range subs {
		replayID := "replay:" + sub.clientID
		req, err := json.Marshal(map[string]any{
			"jsonrpc": "2.0",
			"id":      replayID,
			"method":  "eth_subscribe",
			"params":  sub.subscribeParams,
		})
		if err != nil {
			return fmt.Errorf("marshal replay for %s: %w", sub.clientID, err)
		}
		if err := up.WriteMessage(websocket.TextMessage, req); err != nil {
			return fmt.Errorf("write replay: %w", err)
		}
		pendingReplay[replayID] = sub
	}

	// Phase 2: collect responses. Apply a deadline so we never hang indefinitely.
	up.SetReadDeadline(time.Now().Add(30 * time.Second))
	defer up.SetReadDeadline(time.Time{})

	for len(pendingReplay) > 0 {
		_, msg, err := up.ReadMessage()
		if err != nil {
			return fmt.Errorf("read replay response: %w", err)
		}

		var resp struct {
			ID     json.RawMessage `json:"id"`
			Result string          `json:"result"`
			Err    json.RawMessage `json:"error"`
		}
		if err := json.Unmarshal(msg, &resp); err != nil {
			continue // unparseable frame; skip
		}

		// Match using the string ID we sent ("replay:<clientID>").
		var idStr string
		if json.Unmarshal(resp.ID, &idStr) != nil {
			continue // numeric or null ID — not a replay response; skip
		}
		sub, ok := pendingReplay[idStr]
		if !ok {
			// Early eth_subscription notification or unrelated response — skip.
			// The bidirectional pump will forward such frames once it starts.
			continue
		}

		if len(resp.Err) > 0 && string(resp.Err) != "null" {
			return fmt.Errorf("replay: upstream error for %s: %s", sub.clientID, resp.Err)
		}
		if resp.Result == "" {
			return fmt.Errorf("replay: unexpected response %s", msg)
		}

		s.mu.Lock()
		delete(s.upToClient, sub.upstreamID) // remove stale mapping
		sub.upstreamID = resp.Result
		s.upToClient[resp.Result] = sub.clientID
		s.mu.Unlock()
		delete(pendingReplay, idStr)
	}
	return nil
}

// pump bridges messages between the client (via fromClient channel) and the upstream (up).
// Returns true if the client disconnected or ctx was cancelled, false if the upstream dropped.
//
// s.client is never read here; the caller (run) owns the sole s.client reader goroutine,
// which prevents concurrent reads across reconnections.
func (s *wsSession) pump(ctx context.Context, up *websocket.Conn, fromClient <-chan clientFrame, clientGone <-chan struct{}) (clientLeft bool) {
	upDone := make(chan struct{})

	// upstream → client
	go func() {
		defer close(upDone)
		for {
			mt, msg, err := up.ReadMessage()
			if err != nil {
				return
			}
			if err := s.forwardToClient(msg, mt); err != nil {
				return
			}
		}
	}()

	// client → upstream (via channel; no direct read of s.client here)
	for {
		select {
		case <-upDone:
			return false
		case <-clientGone:
			return true
		case <-ctx.Done():
			return true
		case f, ok := <-fromClient:
			if !ok {
				return true
			}
			if err := s.forwardToUpstream(f.msg, f.mt, up); err != nil {
				return false
			}
		}
	}
}

// forwardToUpstream inspects outgoing messages, tracks eth_subscribe calls, and sends to upstream.
func (s *wsSession) forwardToUpstream(msg []byte, mt int, up *websocket.Conn) error {
	var req struct {
		ID     json.RawMessage `json:"id"`
		Method string          `json:"method"`
		Params json.RawMessage `json:"params"`
	}
	if err := json.Unmarshal(msg, &req); err == nil {
		switch req.Method {
		case "eth_subscribe":
			// Track this request ID so we can record the subscription when the response arrives.
			s.mu.Lock()
			if len(s.pending) >= maxPendingSubscribes {
				s.mu.Unlock()
				log.Printf("[ws] pending subscribe cap (%d) reached, rejecting", maxPendingSubscribes)
				return s.writeToClient(mt, buildErrorResponse(req.ID, "too many pending subscriptions"))
			}
			s.pending[string(req.ID)] = req.Params
			s.mu.Unlock()
		case "eth_unsubscribe":
			// Remap client sub ID → current upstream sub ID before forwarding.
			msg = s.rewriteUnsubscribe(msg)
		}
	}
	return up.WriteMessage(mt, msg)
}

// buildErrorResponse produces a JSON-RPC error envelope echoing the original
// request ID. Used to reject client calls locally without touching the upstream.
func buildErrorResponse(id json.RawMessage, message string) []byte {
	if len(id) == 0 {
		id = json.RawMessage("null")
	}
	resp, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"id":      json.RawMessage(id),
		"error": map[string]any{
			"code":    -32000,
			"message": message,
		},
	})
	if err != nil {
		// Fallback to a static response that is guaranteed to marshal correctly.
		return []byte(`{"jsonrpc":"2.0","id":null,"error":{"code":-32000,"message":"internal error"}}`)
	}
	return resp
}

// forwardToClient rewrites subscription IDs where necessary and sends to client.
func (s *wsSession) forwardToClient(msg []byte, mt int) error {
	var envelope struct {
		ID     json.RawMessage `json:"id"`
		Result json.RawMessage `json:"result"`
		Method string          `json:"method"`
		Params *struct {
			Subscription string `json:"subscription"`
		} `json:"params"`
	}
	if err := json.Unmarshal(msg, &envelope); err != nil {
		return s.writeToClient(mt, msg)
	}

	// Subscription notification: remap upstreamID → clientID when they differ (post-failover).
	// We unmarshal → edit params.subscription → re-marshal to avoid unsafe string substitution
	// that could accidentally match an unrelated field with the same value as the upstream ID.
	if envelope.Method == "eth_subscription" && envelope.Params != nil {
		upID := envelope.Params.Subscription
		s.mu.Lock()
		clientID, ok := s.upToClient[upID]
		s.mu.Unlock()
		if ok && clientID != upID {
			var notif map[string]json.RawMessage
			if err := json.Unmarshal(msg, &notif); err == nil {
				var params map[string]json.RawMessage
				if err := json.Unmarshal(notif["params"], &params); err == nil {
					params["subscription"], _ = json.Marshal(clientID)
					notif["params"], _ = json.Marshal(params)
					if rewritten, err := json.Marshal(notif); err == nil {
						msg = rewritten
					}
				}
			}
		}
		return s.writeToClient(mt, msg)
	}

	// Response to eth_subscribe: record the new subscription using the upstream-assigned ID.
	if len(envelope.ID) > 0 {
		rawID := string(envelope.ID)
		s.mu.Lock()
		params, isPending := s.pending[rawID]
		if isPending {
			delete(s.pending, rawID)
		}
		s.mu.Unlock()

		if isPending {
			var upstreamID string
			if json.Unmarshal(envelope.Result, &upstreamID) == nil && upstreamID != "" {
				sub := &subscription{
					subscribeParams: params,
					clientID:        upstreamID, // client will use the upstream ID as their handle
					upstreamID:      upstreamID,
				}
				s.mu.Lock()
				s.subs[upstreamID] = sub
				s.upToClient[upstreamID] = upstreamID
				s.mu.Unlock()
			}
		}
	}

	return s.writeToClient(mt, msg)
}

// rewriteUnsubscribe replaces the client's subscription ID with the current upstream ID,
// cleans up the subscription state, and returns the rewritten message.
func (s *wsSession) rewriteUnsubscribe(msg []byte) []byte {
	var req struct {
		JSONRPC string          `json:"jsonrpc"`
		ID      json.RawMessage `json:"id"`
		Method  string          `json:"method"`
		Params  []string        `json:"params"`
	}
	if err := json.Unmarshal(msg, &req); err != nil || len(req.Params) == 0 {
		return msg
	}
	clientID := req.Params[0]

	s.mu.Lock()
	sub, ok := s.subs[clientID]
	if ok {
		// Eagerly clean up before forwarding to the upstream. The subscription is
		// effectively gone from the client's perspective the moment it sends
		// eth_unsubscribe. If the message fails to reach the upstream during a
		// concurrent failover, the upstream may push a few stray notifications;
		// because upToClient no longer maps the upstreamID, those frames will be
		// passed through to the client as-is. The client can safely ignore them
		// since it already knows the subscription is cancelled.
		delete(s.upToClient, sub.upstreamID)
		delete(s.subs, clientID)
	}
	s.mu.Unlock()

	if !ok || sub.upstreamID == clientID {
		return msg // IDs already match — no rewrite needed
	}
	req.Params[0] = sub.upstreamID
	rewritten, err := json.Marshal(req)
	if err != nil {
		return msg
	}
	return rewritten
}

// httpToWS converts an HTTP(S) URL to its WebSocket equivalent.
func httpToWS(u string) string {
	switch {
	case strings.HasPrefix(u, "https://"):
		return "wss://" + u[8:]
	case strings.HasPrefix(u, "http://"):
		return "ws://" + u[7:]
	}
	return u
}
