package external

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
)

type Service struct {
	ID        string         `json:"id"`
	BaseURL   string         `json:"base_url"`
	Kind      string         `json:"kind"`
	Status    string         `json:"status"`
	Source    string         `json:"source"`
	Models    []string       `json:"models,omitempty"`
	Metadata  map[string]any `json:"metadata,omitempty"`
	LastError string         `json:"last_error,omitempty"`
}

type ScanOptions struct {
	Endpoints []string
	Ports     []int
	Client    *http.Client
}

func Scan(ctx context.Context, opts ScanOptions) ([]*Service, error) {
	endpoints := scanEndpoints(opts)
	services := make([]*Service, 0, len(endpoints))
	for _, endpoint := range endpoints {
		svc, err := Probe(ctx, endpoint, opts.Client)
		if err != nil {
			continue
		}
		services = append(services, svc)
	}
	sort.Slice(services, func(i, j int) bool {
		return services[i].BaseURL < services[j].BaseURL
	})
	return services, nil
}

func Probe(ctx context.Context, rawBaseURL string, client *http.Client) (*Service, error) {
	baseURL, err := normalizeBaseURL(rawBaseURL)
	if err != nil {
		return nil, err
	}
	if client == nil {
		client = &http.Client{Timeout: 1200 * time.Millisecond}
	}

	if payload, statusCode, err := getJSON(ctx, client, baseURL+"/v1/models"); err == nil && statusCode >= 200 && statusCode < 300 {
		models := extractModels(payload)
		if len(models) > 0 {
			return &Service{
				ID:       serviceID(baseURL),
				BaseURL:  baseURL,
				Kind:     "openai",
				Status:   "reachable",
				Source:   "scan",
				Models:   models,
				Metadata: map[string]any{"models_endpoint": "/v1/models"},
			}, nil
		}
	}

	if payload, statusCode, err := getJSON(ctx, client, baseURL+"/healthz"); err == nil && statusCode >= 200 && statusCode < 300 {
		return &Service{
			ID:       serviceID(baseURL),
			BaseURL:  baseURL,
			Kind:     "healthz",
			Status:   "reachable",
			Source:   "scan",
			Models:   extractModels(payload),
			Metadata: payload,
		}, nil
	}

	return nil, fmt.Errorf("no supported probe endpoint at %s", baseURL)
}

func scanEndpoints(opts ScanOptions) []string {
	seen := make(map[string]struct{})
	var endpoints []string
	add := func(endpoint string) {
		baseURL, err := normalizeBaseURL(endpoint)
		if err != nil {
			return
		}
		if _, exists := seen[baseURL]; exists {
			return
		}
		seen[baseURL] = struct{}{}
		endpoints = append(endpoints, baseURL)
	}
	for _, endpoint := range opts.Endpoints {
		add(endpoint)
	}
	ports := opts.Ports
	if len(ports) == 0 {
		ports = defaultPorts()
	}
	for _, port := range ports {
		add(fmt.Sprintf("http://127.0.0.1:%d", port))
	}
	return endpoints
}

func defaultPorts() []int {
	ports := make([]int, 0, 16)
	for port := 8000; port <= 8010; port++ {
		ports = append(ports, port)
	}
	ports = append(ports, 8080, 7860, 5000, 5001, 3000)
	return ports
}

func normalizeBaseURL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", fmt.Errorf("empty endpoint")
	}
	if !strings.Contains(raw, "://") {
		raw = "http://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "", err
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", fmt.Errorf("unsupported scheme %q", u.Scheme)
	}
	if u.Host == "" {
		return "", fmt.Errorf("missing host")
	}
	u.RawQuery = ""
	u.Fragment = ""
	u.Path = strings.TrimRight(u.Path, "/")
	if strings.HasSuffix(u.Path, "/v1/models") {
		u.Path = strings.TrimSuffix(u.Path, "/v1/models")
	}
	if strings.HasSuffix(u.Path, "/v1") {
		u.Path = strings.TrimSuffix(u.Path, "/v1")
	}
	return strings.TrimRight(u.String(), "/"), nil
}

func serviceID(baseURL string) string {
	sum := sha1.Sum([]byte(baseURL))
	return "external-" + hex.EncodeToString(sum[:8])
}

func getJSON(ctx context.Context, client *http.Client, endpoint string) (map[string]any, int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, 0, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 2*1024*1024))
	if err != nil {
		return nil, resp.StatusCode, err
	}
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, resp.StatusCode, err
	}
	return payload, resp.StatusCode, nil
}

func extractModels(payload map[string]any) []string {
	seen := make(map[string]struct{})
	var models []string
	add := func(value any) {
		if s, ok := value.(string); ok {
			s = strings.TrimSpace(s)
			if s == "" {
				return
			}
			if _, exists := seen[s]; exists {
				return
			}
			seen[s] = struct{}{}
			models = append(models, s)
		}
	}
	var walk func(any)
	walk = func(value any) {
		switch v := value.(type) {
		case []any:
			for _, item := range v {
				walk(item)
			}
		case []string:
			for _, item := range v {
				add(item)
			}
		case map[string]any:
			for _, key := range []string{"id", "model", "model_name", "name"} {
				if value, ok := v[key]; ok {
					add(value)
				}
			}
		}
	}
	if data, ok := payload["data"]; ok {
		walk(data)
	}
	if modelsValue, ok := payload["models"]; ok {
		walk(modelsValue)
	}
	return models
}
