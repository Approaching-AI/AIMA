package agent

import (
	"encoding/json"
	"fmt"
	"strings"
)

// ExplorerToolResult is the result of executing an explorer tool.
type ExplorerToolResult struct {
	Content string
	IsError bool
}

// QueryFunc executes a knowledge base query. Returns JSON result.
type QueryFunc func(qType string, filter map[string]any, limit int) (string, error)

// ExplorerToolExecutor dispatches explorer tool calls.
type ExplorerToolExecutor struct {
	ws      *ExplorerWorkspace
	queryFn QueryFunc
	verdict string
	done    bool
}

// NewExplorerToolExecutor creates a tool executor for the explorer agent.
func NewExplorerToolExecutor(ws *ExplorerWorkspace, queryFn QueryFunc) *ExplorerToolExecutor {
	return &ExplorerToolExecutor{ws: ws, queryFn: queryFn}
}

// Verdict returns the verdict set by the done tool.
func (e *ExplorerToolExecutor) Verdict() string { return e.verdict }

// Done returns true if the done tool was called.
func (e *ExplorerToolExecutor) Done() bool { return e.done }

// Reset clears done/verdict state for a new phase.
func (e *ExplorerToolExecutor) Reset() {
	e.done = false
	e.verdict = ""
}

// Execute dispatches a tool call by name.
func (e *ExplorerToolExecutor) Execute(name string, args json.RawMessage) ExplorerToolResult {
	switch name {
	case "cat":
		return e.execCat(args)
	case "ls":
		return e.execLs(args)
	case "write":
		return e.execWrite(args)
	case "append":
		return e.execAppend(args)
	case "grep":
		return e.execGrep(args)
	case "query":
		return e.execQuery(args)
	case "done":
		return e.execDone(args)
	default:
		return ExplorerToolResult{Content: fmt.Sprintf("unknown tool: %s", name), IsError: true}
	}
}

func (e *ExplorerToolExecutor) execCat(args json.RawMessage) ExplorerToolResult {
	var p struct{ Path string `json:"path"` }
	if err := json.Unmarshal(args, &p); err != nil {
		return ExplorerToolResult{Content: err.Error(), IsError: true}
	}
	content, err := e.ws.ReadFile(p.Path)
	if err != nil {
		return ExplorerToolResult{Content: err.Error(), IsError: true}
	}
	return ExplorerToolResult{Content: content}
}

func (e *ExplorerToolExecutor) execLs(args json.RawMessage) ExplorerToolResult {
	var p struct{ Path string `json:"path"` }
	_ = json.Unmarshal(args, &p)
	if p.Path == "" {
		p.Path = "."
	}
	entries, err := e.ws.ListDir(p.Path)
	if err != nil {
		return ExplorerToolResult{Content: err.Error(), IsError: true}
	}
	return ExplorerToolResult{Content: strings.Join(entries, "\n")}
}

func (e *ExplorerToolExecutor) execWrite(args json.RawMessage) ExplorerToolResult {
	var p struct {
		Path    string `json:"path"`
		Content string `json:"content"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return ExplorerToolResult{Content: err.Error(), IsError: true}
	}
	if err := e.ws.WriteFile(p.Path, p.Content); err != nil {
		return ExplorerToolResult{Content: err.Error(), IsError: true}
	}
	return ExplorerToolResult{Content: "ok"}
}

func (e *ExplorerToolExecutor) execAppend(args json.RawMessage) ExplorerToolResult {
	var p struct {
		Path    string `json:"path"`
		Content string `json:"content"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return ExplorerToolResult{Content: err.Error(), IsError: true}
	}
	if err := e.ws.AppendFile(p.Path, p.Content); err != nil {
		return ExplorerToolResult{Content: err.Error(), IsError: true}
	}
	return ExplorerToolResult{Content: "ok"}
}

func (e *ExplorerToolExecutor) execGrep(args json.RawMessage) ExplorerToolResult {
	var p struct {
		Pattern string `json:"pattern"`
		Path    string `json:"path"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return ExplorerToolResult{Content: err.Error(), IsError: true}
	}
	if p.Path == "" {
		p.Path = "."
	}

	var matches []string
	var err error
	if strings.HasSuffix(p.Path, "/") || p.Path == "." {
		matches, err = e.ws.GrepDir(p.Pattern, p.Path)
	} else {
		matches, err = e.ws.GrepFile(p.Pattern, p.Path)
	}
	if err != nil {
		return ExplorerToolResult{Content: err.Error(), IsError: true}
	}
	if len(matches) == 0 {
		return ExplorerToolResult{Content: "(no matches)"}
	}
	return ExplorerToolResult{Content: strings.Join(matches, "\n")}
}

func (e *ExplorerToolExecutor) execQuery(args json.RawMessage) ExplorerToolResult {
	var p struct {
		Type   string         `json:"type"`
		Filter map[string]any `json:"filter"`
		Limit  int            `json:"limit"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return ExplorerToolResult{Content: err.Error(), IsError: true}
	}
	if e.queryFn == nil {
		return ExplorerToolResult{Content: "query not available (no database)", IsError: true}
	}
	result, err := e.queryFn(p.Type, p.Filter, p.Limit)
	if err != nil {
		return ExplorerToolResult{Content: err.Error(), IsError: true}
	}
	return ExplorerToolResult{Content: result}
}

func (e *ExplorerToolExecutor) execDone(args json.RawMessage) ExplorerToolResult {
	var p struct{ Verdict string `json:"verdict"` }
	_ = json.Unmarshal(args, &p)
	e.done = true
	e.verdict = p.Verdict
	return ExplorerToolResult{Content: "ok"}
}

// ToolDefinitions returns OpenAI-compatible tool definitions for the explorer tools.
func (e *ExplorerToolExecutor) ToolDefinitions() []ToolDefinition {
	return []ToolDefinition{
		{
			Name:        "cat",
			Description: "Read file contents. Path relative to workspace root.",
			InputSchema: json.RawMessage(`{"type":"object","properties":{"path":{"type":"string","description":"File path relative to workspace"}},"required":["path"]}`),
		},
		{
			Name:        "ls",
			Description: "List directory entries. Default: workspace root.",
			InputSchema: json.RawMessage(`{"type":"object","properties":{"path":{"type":"string","description":"Directory path (default: '.')"}}}`),
		},
		{
			Name:        "write",
			Description: "Write content to a file (overwrite). Cannot write AIMA-managed fact documents.",
			InputSchema: json.RawMessage(`{"type":"object","properties":{"path":{"type":"string"},"content":{"type":"string"}},"required":["path","content"]}`),
		},
		{
			Name:        "append",
			Description: "Append content to a file. Cannot write AIMA-managed fact documents.",
			InputSchema: json.RawMessage(`{"type":"object","properties":{"path":{"type":"string"},"content":{"type":"string"}},"required":["path","content"]}`),
		},
		{
			Name:        "grep",
			Description: "Search for pattern in file or directory. Returns matching lines with line numbers.",
			InputSchema: json.RawMessage(`{"type":"object","properties":{"pattern":{"type":"string","description":"Regex pattern"},"path":{"type":"string","description":"File or directory path (default: '.')"}},"required":["pattern"]}`),
		},
		{
			Name:        "query",
			Description: "Query the knowledge base (SQLite, read-only). Types: configurations, benchmarks, advisories, exploration_runs.",
			InputSchema: json.RawMessage(`{"type":"object","properties":{"type":{"type":"string","enum":["configurations","benchmarks","advisories","exploration_runs"]},"filter":{"type":"object"},"limit":{"type":"integer"}},"required":["type"]}`),
		},
		{
			Name:        "done",
			Description: "Signal that the current phase is complete. In Check phase, set verdict to 'continue' or 'done'.",
			InputSchema: json.RawMessage(`{"type":"object","properties":{"verdict":{"type":"string","enum":["continue","done"],"description":"Only for Check phase: continue=need more experiments, done=round complete"}}}`),
		},
	}
}
