package models

import "encoding/json"

// RPCRequest represents a standard JSON-RPC 2.0 request.
// Documentation: https://www.jsonrpc.org/specification
//
// ID uses json.RawMessage to preserve byte-exact form: interface{} would
// lose precision for integers >2^53 and normalise "1.0" to "1".
type RPCRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
	ID      json.RawMessage `json:"id"`
}

// RPCResponse represents a standard JSON-RPC 2.0 response.
type RPCResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *RPCError       `json:"error,omitempty"`
	ID      json.RawMessage `json:"id"`
}

// RPCError represents the error object in a JSON-RPC 2.0 response.
type RPCError struct {
	Code    int         `json:"code"`
	Message string      `json:"message"`
	Data    interface{} `json:"data,omitempty"`
}
