package openclaw

import (
	"os"
	"path/filepath"
)

// Deps holds dependencies for the OpenClaw integration plugin.
// All external access is through interfaces to avoid importing
// proxy or knowledge packages.
type Deps struct {
	Backends   BackendLister
	Catalog    CatalogReader
	ConfigPath string        // e.g. ~/.openclaw/openclaw.json
	ProxyAddr  string        // e.g. "http://127.0.0.1:6188/v1"
	APIKey     func() string // AIMA proxy API key getter (may return empty)
	MCPCommand string        // Absolute path or command name for spawning `aima mcp`
}

// BackendLister provides read-only access to the proxy's backend table.
type BackendLister interface {
	ListBackends() map[string]*Backend
}

// Backend mirrors proxy.Backend fields needed by this plugin.
type Backend struct {
	ModelName           string
	EngineType          string
	Address             string
	Ready               bool
	Remote              bool
	ContextWindowTokens int // actual deployed context window (from aima.dev/context_window label)
}

type RequestPatch struct {
	Path           string
	EnginePrefixes []string
	Body           map[string]any
}

// CatalogReader provides model metadata lookup from the knowledge catalog.
type CatalogReader interface {
	ModelType(name string) string
	ModelContextWindow(name string) int
	ModelFamily(name string) string
	OpenClawRequestPatches(name string) []RequestPatch
}

// DefaultConfigPath returns the default OpenClaw config path (~/.openclaw/openclaw.json).
func DefaultConfigPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".openclaw", "openclaw.json")
}

func (d *Deps) proxyAPIKey() string {
	if d == nil || d.APIKey == nil {
		return ""
	}
	return d.APIKey()
}
