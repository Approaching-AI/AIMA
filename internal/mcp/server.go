package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"sync"

	"github.com/jguan/aima/internal/buildinfo"
)

// JSON-RPC 2.0 types

type jsonrpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type jsonrpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Result  any             `json:"result,omitempty"`
	Error   *jsonrpcError   `json:"error,omitempty"`
}

type jsonrpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// Standard JSON-RPC error codes
const (
	codeParseError     = -32700
	codeInvalidRequest = -32600
	codeMethodNotFound = -32601
	codeInvalidParams  = -32602
	codeInternalError  = -32603
)

// MCP types

// Tool represents an MCP tool definition.
type Tool struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"inputSchema"`
	Handler     ToolHandler     `json:"-"`
}

// ToolHandler processes a tool call and returns content.
type ToolHandler func(ctx context.Context, params json.RawMessage) (*ToolResult, error)

// ToolResult is what a tool returns.
type ToolResult struct {
	Content []ContentBlock `json:"content"`
	IsError bool           `json:"isError,omitempty"`
}

// ContentBlock is a single content item in a tool result.
type ContentBlock struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// TextResult creates a ToolResult with a single text block.
func TextResult(text string) *ToolResult {
	return &ToolResult{
		Content: []ContentBlock{{Type: "text", Text: text}},
	}
}

// ErrorResult creates an error ToolResult with a single text block.
func ErrorResult(text string) *ToolResult {
	return &ToolResult{
		Content: []ContentBlock{{Type: "text", Text: text}},
		IsError: true,
	}
}

// Server handles MCP JSON-RPC 2.0 communication.
type Server struct {
	tools   map[string]*Tool
	mu      sync.RWMutex
	profile Profile
}

// NewServer creates a new MCP server.
func NewServer() *Server {
	return &Server{
		tools: make(map[string]*Tool),
	}
}

// SetProfile sets the tool discovery profile used by MCP tools/list responses.
// Internal callers still use ListTools and ExecuteTool to access the full registry.
func (s *Server) SetProfile(p Profile) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.profile = p
}

// RegisterTool adds a tool to the server.
func (s *Server) RegisterTool(tool *Tool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.tools[tool.Name] = tool
}

// ServeStdio reads JSON-RPC from os.Stdin, writes to os.Stdout (blocking).
func (s *Server) ServeStdio(ctx context.Context) error {
	return s.ServeIO(ctx, os.Stdin, os.Stdout)
}

// ServeIO reads JSON-RPC from the given reader, writes to the given writer.
// If reader or writer is nil, os.Stdin/os.Stdout is used.
func (s *Server) ServeIO(ctx context.Context, r io.Reader, w io.Writer) error {
	if r == nil || w == nil {
		return fmt.Errorf("reader and writer must not be nil for ServeIO")
	}
	scanner := bufio.NewScanner(r)
	// Allow large messages (up to 10MB)
	scanner.Buffer(make([]byte, 0, 64*1024), 10*1024*1024)

	for scanner.Scan() {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}

		resp, err := s.HandleMessage(ctx, line)
		if err != nil {
			slog.Error("handle message", "error", err)
			continue
		}
		if resp == nil {
			continue // notification, no response needed
		}

		resp = append(resp, '\n')
		if _, err := w.Write(resp); err != nil {
			return fmt.Errorf("write response: %w", err)
		}
	}

	if err := scanner.Err(); err != nil {
		return fmt.Errorf("read input: %w", err)
	}
	return nil
}

// ServeHTTP handles SSE transport for MCP.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodPost {
		s.handleHTTPPost(w, r)
		return
	}
	http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
}

func (s *Server) handleHTTPPost(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 10*1024*1024))
	if err != nil {
		http.Error(w, "read body failed", http.StatusBadRequest)
		return
	}
	defer r.Body.Close()

	resp, err := s.HandleMessage(r.Context(), body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.Write(resp)
}

// HandleMessage processes a single JSON-RPC message and returns the response bytes.
func (s *Server) HandleMessage(ctx context.Context, msg []byte) ([]byte, error) {
	var req jsonrpcRequest
	if err := json.Unmarshal(msg, &req); err != nil {
		return marshalResponse(jsonrpcResponse{
			JSONRPC: "2.0",
			Error:   &jsonrpcError{Code: codeParseError, Message: "Parse error"},
		})
	}

	if req.JSONRPC != "2.0" {
		return marshalResponse(jsonrpcResponse{
			JSONRPC: "2.0",
			ID:      req.ID,
			Error:   &jsonrpcError{Code: codeInvalidRequest, Message: "Invalid request: jsonrpc must be 2.0"},
		})
	}

	switch req.Method {
	case "initialize":
		return s.handleInitialize(req.ID)
	case "notifications/initialized":
		return nil, nil // notification, no response
	case "tools/list":
		return s.handleToolsList(req.ID)
	case "tools/call":
		return s.handleToolsCall(ctx, req.ID, req.Params)
	case "ping":
		return s.handlePing(req.ID)
	default:
		return marshalResponse(jsonrpcResponse{
			JSONRPC: "2.0",
			ID:      req.ID,
			Error:   &jsonrpcError{Code: codeMethodNotFound, Message: fmt.Sprintf("Method not found: %s", req.Method)},
		})
	}
}

func (s *Server) handleInitialize(id json.RawMessage) ([]byte, error) {
	result := map[string]any{
		"protocolVersion": "2024-11-05",
		"capabilities": map[string]any{
			"tools": map[string]any{},
		},
		"serverInfo": map[string]any{
			"name":    "aima",
			"version": buildinfo.Version,
		},
	}
	return marshalResponse(jsonrpcResponse{
		JSONRPC: "2.0",
		ID:      id,
		Result:  result,
	})
}

func (s *Server) handlePing(id json.RawMessage) ([]byte, error) {
	return marshalResponse(jsonrpcResponse{
		JSONRPC: "2.0",
		ID:      id,
		Result:  map[string]any{},
	})
}

func (s *Server) handleToolsList(id json.RawMessage) ([]byte, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	tools := make([]map[string]any, 0, len(s.tools))
	for _, t := range s.tools {
		if !ProfileMatches(s.profile, t.Name) {
			continue
		}
		tools = append(tools, map[string]any{
			"name":        t.Name,
			"description": t.Description,
			"inputSchema": json.RawMessage(t.InputSchema),
		})
	}

	return marshalResponse(jsonrpcResponse{
		JSONRPC: "2.0",
		ID:      id,
		Result:  map[string]any{"tools": tools},
	})
}

type toolsCallParams struct {
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}

func (s *Server) handleToolsCall(ctx context.Context, id json.RawMessage, params json.RawMessage) ([]byte, error) {
	var p toolsCallParams
	if err := json.Unmarshal(params, &p); err != nil {
		return marshalResponse(jsonrpcResponse{
			JSONRPC: "2.0",
			ID:      id,
			Error:   &jsonrpcError{Code: codeInvalidParams, Message: "Invalid params: " + err.Error()},
		})
	}

	s.mu.RLock()
	tool, ok := s.tools[p.Name]
	s.mu.RUnlock()

	if !ok {
		return marshalResponse(jsonrpcResponse{
			JSONRPC: "2.0",
			ID:      id,
			Error:   &jsonrpcError{Code: codeMethodNotFound, Message: fmt.Sprintf("Tool not found: %s", p.Name)},
		})
	}

	result, err := tool.Handler(ctx, p.Arguments)
	if err != nil {
		return marshalResponse(jsonrpcResponse{
			JSONRPC: "2.0",
			ID:      id,
			Result: &ToolResult{
				Content: []ContentBlock{{Type: "text", Text: err.Error()}},
				IsError: true,
			},
		})
	}

	return marshalResponse(jsonrpcResponse{
		JSONRPC: "2.0",
		ID:      id,
		Result:  result,
	})
}

// ExecuteTool calls a tool handler directly (used by Agent).
func (s *Server) ExecuteTool(ctx context.Context, name string, arguments json.RawMessage) (*ToolResult, error) {
	s.mu.RLock()
	tool, ok := s.tools[name]
	s.mu.RUnlock()

	if !ok {
		return nil, fmt.Errorf("tool not found: %s", name)
	}

	return tool.Handler(ctx, arguments)
}

// ListTools returns all registered tool definitions.
func (s *Server) ListTools() []ToolDefinition {
	s.mu.RLock()
	defer s.mu.RUnlock()

	defs := make([]ToolDefinition, 0, len(s.tools))
	for _, t := range s.tools {
		defs = append(defs, ToolDefinition{
			Name:        t.Name,
			Description: t.Description,
			InputSchema: t.InputSchema,
		})
	}
	return defs
}

// ToolDefinition is a serializable tool description (no handler).
type ToolDefinition struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"inputSchema"`
}

func marshalResponse(resp jsonrpcResponse) ([]byte, error) {
	data, err := json.Marshal(resp)
	if err != nil {
		return nil, fmt.Errorf("marshal response: %w", err)
	}
	return data, nil
}
