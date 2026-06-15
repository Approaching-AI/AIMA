package openclaw

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestProbeProxyReachable(t *testing.T) {
	clearProxyEnv(t)

	// Server up: any HTTP status (even 401) proves serve is listening.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	addr := srv.URL + "/v1"

	reachable, warning := probeProxyReachable(context.Background(), addr, "key")
	if !reachable {
		t.Errorf("server up: want reachable, got unreachable (warning=%q)", warning)
	}
	if warning != "" {
		t.Errorf("server up: want no warning, got %q", warning)
	}

	// Same address after Close → connection refused → unreachable, with an
	// actionable warning that names the data-plane fix.
	srv.Close()
	reachable, warning = probeProxyReachable(context.Background(), addr, "key")
	if reachable {
		t.Error("server down: want unreachable")
	}
	if !strings.Contains(warning, "aima serve") {
		t.Errorf("server down: warning should point at `aima serve`, got %q", warning)
	}

	// Empty address: no listener to check, never a false alarm.
	reachable, warning = probeProxyReachable(context.Background(), "", "")
	if !reachable || warning != "" {
		t.Errorf("empty addr: want reachable/no-warning, got reachable=%v warning=%q", reachable, warning)
	}
}

func TestProbeProxyReachableDetectsLoopbackProxyInterception(t *testing.T) {
	clearProxyEnv(t)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	t.Setenv("HTTP_PROXY", "http://127.0.0.1:1")
	reachable, warning := probeProxyReachable(context.Background(), srv.URL, "")
	if reachable {
		t.Fatal("bad HTTP_PROXY without NO_PROXY: want unreachable")
	}
	if !strings.Contains(warning, "NO_PROXY=127.0.0.1") {
		t.Errorf("proxy warning should explain NO_PROXY fix, got %q", warning)
	}

	t.Setenv("NO_PROXY", "127.0.0.1,localhost,::1")
	reachable, warning = probeProxyReachable(context.Background(), srv.URL, "")
	if !reachable || warning != "" {
		t.Fatalf("NO_PROXY covering loopback: want reachable/no-warning, got reachable=%v warning=%q", reachable, warning)
	}
}

func clearProxyEnv(t *testing.T) {
	t.Helper()
	for _, key := range []string{
		"HTTP_PROXY", "http_proxy",
		"HTTPS_PROXY", "https_proxy",
		"ALL_PROXY", "all_proxy",
		"NO_PROXY", "no_proxy",
	} {
		t.Setenv(key, "")
	}
}
