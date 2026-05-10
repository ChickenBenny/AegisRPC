package proxy

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
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
	subs       map[string]*subscription   // clientID → subscription
	upToClient map[string]string          // upstreamID → clientID (updated on failover)
	pending    map[string]json.RawMessage // raw request ID → subscribe params (awaiting response)

	// Heartbeat timing. Defaults set by newWSSession; tests override for speed.
	pingPeriod time.Duration // how often to send a Ping to the upstream
	pongWait   time.Duration // how long to wait for the Pong before declaring the upstream dead

	// replayPendingCap bounds how many upstream frames may be buffered
	// during reconnect / subscription replay before the session starts
	// dropping new arrivals (audit #5). 0 disables the safety net.
	replayPendingCap int
}

// clientFrame is one WebSocket frame received from the client.
type clientFrame struct {
	mt  int
	msg []byte
}

func newWSSession(pool *upstream.Pool, client *websocket.Conn, replayPendingCap int) *wsSession {
	return &wsSession{
		pool:             pool,
		client:           client,
		subs:             make(map[string]*subscription),
		upToClient:       make(map[string]string),
		pending:          make(map[string]json.RawMessage),
		pingPeriod:       upstreamPingPeriod,
		pongWait:         upstreamPongWait,
		replayPendingCap: replayPendingCap,
	}
}

// clientWriteTimeout is the maximum time allowed for a single write to the client.
// If the client's TCP window is full for longer than this, the session is terminated
// so a stalled client cannot block the upstream reader goroutine indefinitely.
const clientWriteTimeout = 10 * time.Second

// upstreamPingPeriod is how often AegisRPC sends a WebSocket Ping to the upstream.
const upstreamPingPeriod = 30 * time.Second

// upstreamPongWait is how long we wait for a Pong reply before declaring the
// upstream dead and triggering failover.
const upstreamPongWait = 10 * time.Second

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
//
// replayPendingCap bounds how many upstream frames each session may buffer
// during the reconnect / subscription replay window (audit #5).
func ServeWS(pool *upstream.Pool, replayPendingCap int) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			slog.Error("websocket upgrade failed", "err", err)
			return
		}
		defer conn.Close()
		newWSSession(pool, conn, replayPendingCap).run(r.Context())
	}
}

// run is the session main loop. It connects to an upstream, pumps messages bidirectionally,
// and reconnects transparently whenever the upstream drops.
//
// A single goroutine owns s.client reads for the entire session lifetime, ensuring
// gorilla/websocket's "one concurrent reader" contract is never violated across reconnects.
func (s *wsSession) run(ctx context.Context) {
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
			slog.Warn("no upstream available for ws session", "err", err)
			_ = s.writeToClient(websocket.CloseMessage,
				websocket.FormatCloseMessage(websocket.CloseInternalServerErr, "no upstream"))
			return
		}

		pendingFrames, err := s.replaySubscriptions(up)
		if err != nil {
			slog.Warn("subscription replay failed", "err", err)
			up.Close()
			continue
		}
		// Flush frames that arrived during the replay window (audit #5).
		for _, f := range pendingFrames {
			if werr := s.forwardToClient(f.msg, f.mt); werr != nil {
				up.Close()
				return
			}
		}

		clientLeft := s.pump(ctx, up, fromClient, clientGone)
		up.Close()

		if clientLeft {
			return
		}
		slog.Info("ws upstream dropped, reconnecting")
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
			slog.Warn("ws upstream dial failed, trying next", "url", wsURL, "err", err)
			continue
		}
		return conn, nil
	}
	return nil, fmt.Errorf("no reachable upstream")
}

// replaySubscriptions re-sends every known subscription to a fresh upstream
// connection and updates the upstreamID mapping. Non-replay frames that
// arrive during the window (real notifications, other responses) are
// returned in the second slice so the caller can flush them to the client
// before pump starts — pre-fix these were silently dropped (audit #5).
func (s *wsSession) replaySubscriptions(up *websocket.Conn) ([]clientFrame, error) {
	s.mu.Lock()
	subs := make([]*subscription, 0, len(s.subs))
	for _, sub := range s.subs {
		subs = append(subs, sub)
	}
	s.mu.Unlock()

	if len(subs) == 0 {
		return nil, nil
	}

	pendingReplay := make(map[string]*subscription, len(subs))

	for _, sub := range subs {
		replayID := "replay:" + sub.clientID
		req, err := json.Marshal(map[string]any{
			"jsonrpc": "2.0",
			"id":      replayID,
			"method":  "eth_subscribe",
			"params":  sub.subscribeParams,
		})
		if err != nil {
			return nil, fmt.Errorf("marshal replay for %s: %w", sub.clientID, err)
		}
		if err := up.WriteMessage(websocket.TextMessage, req); err != nil {
			return nil, fmt.Errorf("write replay: %w", err)
		}
		pendingReplay[replayID] = sub
	}

	// Collect responses.
	up.SetReadDeadline(time.Now().Add(30 * time.Second))
	defer up.SetReadDeadline(time.Time{})

	var pendingFrames []clientFrame
	var capWarned bool
	stash := func(mt int, msg []byte) {
		if s.replayPendingCap > 0 && len(pendingFrames) >= s.replayPendingCap {
			if !capWarned {
				slog.Warn("ws replay pending queue overflowed; dropping new frames",
					"cap", s.replayPendingCap, "subscriptions", len(subs))
				capWarned = true
			}
			return
		}
		// gorilla reuses the read buffer between calls, so the bytes must
		// be copied before being held across iterations.
		msgCopy := make([]byte, len(msg))
		copy(msgCopy, msg)
		pendingFrames = append(pendingFrames, clientFrame{mt: mt, msg: msgCopy})
	}

	for len(pendingReplay) > 0 {
		mt, msg, err := up.ReadMessage()
		if err != nil {
			return pendingFrames, fmt.Errorf("read replay response: %w", err)
		}

		var resp struct {
			ID     json.RawMessage `json:"id"`
			Result string          `json:"result"`
			Err    json.RawMessage `json:"error"`
		}
		if err := json.Unmarshal(msg, &resp); err != nil {
			stash(mt, msg)
			continue
		}

		// Match using the string ID we sent ("replay:<clientID>").
		var idStr string
		if json.Unmarshal(resp.ID, &idStr) != nil {
			stash(mt, msg)
			continue
		}
		sub, ok := pendingReplay[idStr]
		if !ok {
			stash(mt, msg)
			continue
		}

		if len(resp.Err) > 0 && string(resp.Err) != "null" {
			return pendingFrames, fmt.Errorf("replay: upstream error for %s: %s", sub.clientID, resp.Err)
		}
		if resp.Result == "" {
			return pendingFrames, fmt.Errorf("replay: unexpected response %s", msg)
		}

		s.mu.Lock()
		delete(s.upToClient, sub.upstreamID)
		sub.upstreamID = resp.Result
		s.upToClient[resp.Result] = sub.clientID
		s.mu.Unlock()
		delete(pendingReplay, idStr)
	}
	return pendingFrames, nil
}

// pump bridges messages between the client (via fromClient channel) and the upstream (up).
// Returns true if the client disconnected or ctx was cancelled, false if the upstream dropped.
//
// s.client is never read here; the caller (run) owns the sole s.client reader goroutine,
// which prevents concurrent reads across reconnections.
//
// Heartbeat: a Ping is sent to the upstream every s.pingPeriod. If no Pong arrives within
// s.pongWait the read deadline expires, the upstream goroutine exits, and pump returns false
// to trigger failover.
func (s *wsSession) pump(ctx context.Context, up *websocket.Conn, fromClient <-chan clientFrame, clientGone <-chan struct{}) (clientLeft bool) {
	upDone := make(chan struct{})

	// Initial deadline so DOA upstreams time out within one heartbeat
	// (audit #14). All later updates happen in the reader goroutine via
	// the pong handler — gorilla forbids concurrent read-side calls
	// (audit #4).
	up.SetReadDeadline(time.Now().Add(s.pingPeriod + s.pongWait))
	up.SetPongHandler(func(string) error {
		up.SetReadDeadline(time.Now().Add(s.pingPeriod + s.pongWait))
		return nil
	})

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

	pingTicker := time.NewTicker(s.pingPeriod)
	defer pingTicker.Stop()

	for {
		select {
		case <-upDone:
			return false
		case <-clientGone:
			return true
		case <-ctx.Done():
			return true
		case <-pingTicker.C:
			// WriteControl is the one exception gorilla guarantees is
			// concurrency-safe with read-side methods.
			if err := up.WriteControl(websocket.PingMessage, []byte{}, time.Now().Add(s.pongWait)); err != nil {
				up.Close()
				<-upDone
				return false
			}
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
			s.mu.Lock()
			if len(s.pending) >= maxPendingSubscribes {
				s.mu.Unlock()
				slog.Warn("ws pending subscribe cap reached, rejecting", "cap", maxPendingSubscribes)
				return s.writeToClient(mt, buildErrorResponse(req.ID, "too many pending subscriptions"))
			}
			s.pending[string(req.ID)] = req.Params
			s.mu.Unlock()
		case "eth_unsubscribe":
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
					clientID:        upstreamID,
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
		return msg
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
