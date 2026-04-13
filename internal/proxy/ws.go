package proxy

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
	"sync"

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
			_ = s.client.WriteMessage(websocket.CloseMessage,
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
		conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
		if err != nil {
			log.Printf("[ws] dial %s failed: %v, trying next", wsURL, err)
			continue
		}
		return conn, nil
	}
	return nil, fmt.Errorf("no reachable upstream")
}

// replaySubscriptions re-sends every known subscription to a fresh upstream connection.
// It reads each response synchronously (before the bidirectional pump starts) and updates
// the upstreamID mapping so that future notifications are remapped correctly.
func (s *wsSession) replaySubscriptions(up *websocket.Conn) error {
	s.mu.Lock()
	subs := make([]*subscription, 0, len(s.subs))
	for _, sub := range s.subs {
		subs = append(subs, sub)
	}
	s.mu.Unlock()

	for _, sub := range subs {
		req, _ := json.Marshal(map[string]any{
			"jsonrpc": "2.0",
			"id":      "replay:" + sub.clientID,
			"method":  "eth_subscribe",
			"params":  sub.subscribeParams,
		})
		if err := up.WriteMessage(websocket.TextMessage, req); err != nil {
			return fmt.Errorf("write replay: %w", err)
		}
		_, msg, err := up.ReadMessage()
		if err != nil {
			return fmt.Errorf("read replay response: %w", err)
		}
		var resp struct {
			Result string `json:"result"`
		}
		if err := json.Unmarshal(msg, &resp); err != nil || resp.Result == "" {
			return fmt.Errorf("replay: unexpected response %s", msg)
		}

		s.mu.Lock()
		delete(s.upToClient, sub.upstreamID) // remove stale mapping
		sub.upstreamID = resp.Result
		s.upToClient[resp.Result] = sub.clientID
		s.mu.Unlock()
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
			s.pending[string(req.ID)] = req.Params
			s.mu.Unlock()
		case "eth_unsubscribe":
			// Remap client sub ID → current upstream sub ID before forwarding.
			msg = s.rewriteUnsubscribe(msg)
		}
	}
	return up.WriteMessage(mt, msg)
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
		return s.client.WriteMessage(mt, msg)
	}

	// Subscription notification: remap upstreamID → clientID when they differ (post-failover).
	if envelope.Method == "eth_subscription" && envelope.Params != nil {
		upID := envelope.Params.Subscription
		s.mu.Lock()
		clientID, ok := s.upToClient[upID]
		s.mu.Unlock()
		if ok && clientID != upID {
			msg = bytes.Replace(msg, []byte(`"`+upID+`"`), []byte(`"`+clientID+`"`), 1)
		}
		return s.client.WriteMessage(mt, msg)
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

	return s.client.WriteMessage(mt, msg)
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
