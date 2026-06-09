package external

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"net/url"
	"strings"
	"time"

	state "github.com/jguan/aima/internal"
	"github.com/jguan/aima/internal/proxy"
)

type Store interface {
	ListExternalServices(ctx context.Context) ([]*state.ExternalService, error)
	UpsertExternalService(ctx context.Context, svc *state.ExternalService) error
	SetExternalServiceImportedModels(ctx context.Context, idOrBaseURL string, imported bool, models []string) error
	SetExternalServiceStatus(ctx context.Context, idOrBaseURL, status, lastError string) error
}

type Reconciler struct {
	store Store
	proxy *proxy.Server
}

type Overview struct {
	ID             string         `json:"id"`
	BaseURL        string         `json:"base_url"`
	Kind           string         `json:"kind"`
	Status         string         `json:"status"`
	Source         string         `json:"source"`
	Models         []string       `json:"models,omitempty"`
	ImportedModels []string       `json:"imported_models,omitempty"`
	Metadata       map[string]any `json:"metadata,omitempty"`
	LastError      string         `json:"last_error,omitempty"`
	Imported       bool           `json:"imported"`
	FirstSeenAt    time.Time      `json:"first_seen_at,omitempty"`
	LastSeenAt     time.Time      `json:"last_seen_at,omitempty"`
}

type ImportResult struct {
	Imported bool     `json:"imported"`
	Count    int      `json:"count"`
	Service  Overview `json:"service"`
}

func NewReconciler(store Store, proxyServer *proxy.Server) *Reconciler {
	return &Reconciler{store: store, proxy: proxyServer}
}

func (r *Reconciler) Scan(ctx context.Context) ([]Overview, error) {
	existing, err := r.store.ListExternalServices(ctx)
	if err != nil {
		return nil, err
	}
	endpoints := make([]string, 0)
	for _, svc := range existing {
		if svc.Imported && strings.TrimSpace(svc.BaseURL) != "" {
			endpoints = append(endpoints, svc.BaseURL)
		}
	}
	services, err := Scan(ctx, ScanOptions{Endpoints: endpoints})
	if err != nil {
		return nil, err
	}
	own := r.ownDeploymentAddrs()
	out := make([]Overview, 0, len(services))
	for _, svc := range services {
		// Skip AIMA's own deployment backends (e.g. native llama.cpp on :8080);
		// they are managed deployments, not importable external services.
		if _, ok := own[normalizeHostPort(svc.BaseURL)]; ok {
			continue
		}
		overview := OverviewFromScan(svc)
		if err := r.store.UpsertExternalService(ctx, RecordFromOverview(overview)); err != nil {
			slog.Warn("external service scan: failed to persist service", "base_url", svc.BaseURL, "error", err)
			continue
		}
		out = append(out, overview)
	}
	reachable := make(map[string]struct{}, len(out))
	for _, svc := range out {
		reachable[svc.BaseURL] = struct{}{}
	}
	// Mark previously-discovered services that have disappeared (e.g. an
	// undeployed model's backend) unreachable so they stop being listed.
	for _, url := range staleScannedAddrs(existing, reachable, own) {
		if err := r.store.SetExternalServiceStatus(ctx, url, "unreachable", "not found in last scan"); err != nil {
			slog.Warn("external service scan: failed to mark stale service unreachable", "base_url", url, "error", err)
		}
	}
	if err := r.Restore(ctx, reachable); err != nil {
		slog.Warn("external service scan: failed to restore imported services", "error", err)
	}
	return out, nil
}

func (r *Reconciler) List(ctx context.Context) ([]Overview, error) {
	rows, err := r.store.ListExternalServices(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]Overview, 0, len(rows))
	for _, row := range rows {
		ov := OverviewFromRecord(row)
		// Hide auto-discovered services that have vanished (dead scanned, not
		// imported) — e.g. the backend of a model the user just undeployed.
		if ov.Source == "scan" && !ov.Imported && strings.EqualFold(ov.Status, "unreachable") {
			continue
		}
		out = append(out, ov)
	}
	// Hide AIMA's own deployment backends that a prior scan may have recorded
	// (e.g. a native llama.cpp deployment on llama.cpp's default port 8080).
	return filterOutOwnDeployments(out, r.ownDeploymentAddrs()), nil
}

func (r *Reconciler) Import(ctx context.Context, idOrBaseURL string, models []string) (ImportResult, error) {
	rows, err := r.store.ListExternalServices(ctx)
	if err != nil {
		return ImportResult{}, err
	}
	var selected Overview
	for _, row := range rows {
		if row.ID == idOrBaseURL || row.BaseURL == idOrBaseURL {
			selected = OverviewFromRecord(row)
			break
		}
	}
	if selected.BaseURL == "" {
		return ImportResult{}, fmt.Errorf("external service %q not found; run external.scan first", idOrBaseURL)
	}
	probed, err := Probe(ctx, selected.BaseURL, nil)
	if err != nil {
		return ImportResult{}, fmt.Errorf("probe external service %s: %w", selected.BaseURL, err)
	}
	selected = OverviewFromScan(probed)
	if err := r.store.UpsertExternalService(ctx, RecordFromOverview(selected)); err != nil {
		return ImportResult{}, err
	}
	importedModels := selectAdvertisedModels(selected.Models, models)
	if len(importedModels) == 0 {
		return ImportResult{}, fmt.Errorf("external service %s has no matching models to import", selected.BaseURL)
	}
	imported, err := ReconcileBackends(r.proxy, selected, importedModels)
	if err != nil {
		return ImportResult{}, err
	}
	if err := r.store.SetExternalServiceImportedModels(ctx, selected.BaseURL, true, importedModels); err != nil {
		return ImportResult{}, err
	}
	selected.Imported = true
	selected.ImportedModels = importedModels
	return ImportResult{Imported: true, Count: imported, Service: selected}, nil
}

func (r *Reconciler) Restore(ctx context.Context, reachable map[string]struct{}) error {
	rows, err := r.store.ListExternalServices(ctx)
	if err != nil {
		return err
	}
	for _, row := range rows {
		if !row.Imported {
			continue
		}
		overview := OverviewFromRecord(row)
		if reachable != nil {
			if _, ok := reachable[row.BaseURL]; !ok {
				CleanupBackendsExcept(r.proxy, overview, nil)
				if err := r.store.SetExternalServiceStatus(ctx, row.BaseURL, "unreachable", "last scan did not reach this imported service"); err != nil {
					slog.Warn("external service restore: failed to mark service unreachable", "base_url", row.BaseURL, "error", err)
				}
				continue
			}
		}
		importedModels := restoreModelSelection(overview.Models, overview.ImportedModels)
		if !sameStringSet(importedModels, overview.ImportedModels) {
			if err := r.store.SetExternalServiceImportedModels(ctx, overview.BaseURL, true, importedModels); err != nil {
				slog.Warn("external service restore: failed to prune imported models", "base_url", overview.BaseURL, "error", err)
			}
		}
		if _, err := ReconcileBackends(r.proxy, overview, importedModels); err != nil {
			slog.Warn("external service restore: failed to register backend", "base_url", overview.BaseURL, "error", err)
		}
	}
	return nil
}

func OverviewFromScan(svc *Service) Overview {
	if svc == nil {
		return Overview{}
	}
	return Overview{
		ID:        svc.ID,
		BaseURL:   svc.BaseURL,
		Kind:      svc.Kind,
		Status:    svc.Status,
		Source:    svc.Source,
		Models:    svc.Models,
		Metadata:  svc.Metadata,
		LastError: svc.LastError,
	}
}

func RecordFromOverview(svc Overview) *state.ExternalService {
	modelsJSON, _ := json.Marshal(svc.Models)
	if len(modelsJSON) == 0 || string(modelsJSON) == "null" {
		modelsJSON = []byte("[]")
	}
	importedModelsJSON, _ := json.Marshal(svc.ImportedModels)
	if len(importedModelsJSON) == 0 || string(importedModelsJSON) == "null" {
		importedModelsJSON = []byte("[]")
	}
	metadataJSON, _ := json.Marshal(svc.Metadata)
	if len(metadataJSON) == 0 || string(metadataJSON) == "null" {
		metadataJSON = []byte("{}")
	}
	return &state.ExternalService{
		ID:                 svc.ID,
		BaseURL:            svc.BaseURL,
		Kind:               svc.Kind,
		Status:             svc.Status,
		Source:             svc.Source,
		ModelsJSON:         string(modelsJSON),
		MetadataJSON:       string(metadataJSON),
		LastError:          svc.LastError,
		Imported:           svc.Imported,
		ImportedModelsJSON: string(importedModelsJSON),
	}
}

func OverviewFromRecord(row *state.ExternalService) Overview {
	if row == nil {
		return Overview{}
	}
	var models []string
	_ = json.Unmarshal([]byte(row.ModelsJSON), &models)
	var importedModels []string
	_ = json.Unmarshal([]byte(row.ImportedModelsJSON), &importedModels)
	var metadata map[string]any
	_ = json.Unmarshal([]byte(row.MetadataJSON), &metadata)
	return Overview{
		ID:             row.ID,
		BaseURL:        row.BaseURL,
		Kind:           row.Kind,
		Status:         row.Status,
		Source:         row.Source,
		Models:         models,
		ImportedModels: importedModels,
		Metadata:       metadata,
		LastError:      row.LastError,
		Imported:       row.Imported,
		FirstSeenAt:    row.FirstSeenAt,
		LastSeenAt:     row.LastSeenAt,
	}
}

func ReconcileBackends(proxyServer *proxy.Server, service Overview, models []string) (int, error) {
	if proxyServer == nil {
		return 0, fmt.Errorf("proxy server is nil")
	}
	if strings.TrimSpace(service.Kind) != "openai" {
		return 0, fmt.Errorf("external service %s is %q, not importable", service.BaseURL, service.Kind)
	}
	models = uniqueStrings(models)
	CleanupBackendsExcept(proxyServer, service, models)
	if len(models) == 0 {
		return 0, nil
	}
	scheme, address, basePath, err := RouteTarget(service.BaseURL)
	if err != nil {
		return 0, err
	}
	imported := 0
	for _, model := range models {
		proxyServer.RegisterBackend(model, &proxy.Backend{
			ModelName:     model,
			UpstreamModel: model,
			EngineType:    "external-openai",
			Scheme:        scheme,
			Address:       address,
			BasePath:      basePath,
			Ready:         true,
			External:      true,
		})
		imported++
	}
	return imported, nil
}

func CleanupBackendsExcept(proxyServer *proxy.Server, service Overview, keepModels []string) {
	if proxyServer == nil {
		return
	}
	scheme, address, basePath, err := RouteTarget(service.BaseURL)
	if err != nil {
		return
	}
	keep := make(map[string]struct{}, len(keepModels))
	for _, model := range keepModels {
		model = strings.TrimSpace(strings.ToLower(model))
		if model != "" {
			keep[model] = struct{}{}
		}
	}
	for model, backend := range proxyServer.ListBackends() {
		if backend == nil || !backend.External {
			continue
		}
		if backend.Address != address || backend.BasePath != basePath {
			continue
		}
		backendScheme := strings.TrimSpace(strings.ToLower(backend.Scheme))
		if backendScheme != "" && backendScheme != scheme {
			continue
		}
		if _, ok := keep[strings.ToLower(model)]; ok {
			continue
		}
		proxyServer.RemoveBackend(model)
	}
}

func RouteTarget(baseURL string) (scheme, address, basePath string, err error) {
	raw := strings.TrimSpace(baseURL)
	if raw == "" {
		return "", "", "", fmt.Errorf("base_url is required")
	}
	if !strings.Contains(raw, "://") {
		raw = "http://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "", "", "", fmt.Errorf("parse base_url %q: %w", baseURL, err)
	}
	if u.Host == "" {
		return "", "", "", fmt.Errorf("base_url %q has no host", baseURL)
	}
	scheme = strings.ToLower(strings.TrimSpace(u.Scheme))
	if scheme == "" {
		scheme = "http"
	}
	if scheme != "http" && scheme != "https" {
		return "", "", "", fmt.Errorf("base_url %q has unsupported scheme %q", baseURL, u.Scheme)
	}
	basePath = strings.TrimRight(u.Path, "/")
	if basePath == "/v1" {
		basePath = ""
	}
	return scheme, u.Host, basePath, nil
}

func selectAdvertisedModels(advertised, requested []string) []string {
	advertised = uniqueStrings(advertised)
	if len(requested) == 0 {
		return advertised
	}
	byKey := make(map[string]string, len(advertised))
	for _, model := range advertised {
		byKey[strings.ToLower(model)] = model
	}
	selected := make([]string, 0, len(requested))
	seen := make(map[string]struct{}, len(requested))
	for _, model := range requested {
		key := strings.ToLower(strings.TrimSpace(model))
		if key == "" {
			continue
		}
		canonical, ok := byKey[key]
		if !ok {
			continue
		}
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		selected = append(selected, canonical)
	}
	return selected
}

func restoreModelSelection(advertised, imported []string) []string {
	advertised = uniqueStrings(advertised)
	imported = uniqueStrings(imported)
	if len(imported) == 0 {
		return advertised
	}
	return selectAdvertisedModels(advertised, imported)
}

func uniqueStrings(values []string) []string {
	out := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		key := strings.ToLower(value)
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, value)
	}
	return out
}

func sameStringSet(a, b []string) bool {
	a = uniqueStrings(a)
	b = uniqueStrings(b)
	if len(a) != len(b) {
		return false
	}
	seen := make(map[string]struct{}, len(a))
	for _, value := range a {
		seen[strings.ToLower(value)] = struct{}{}
	}
	for _, value := range b {
		if _, ok := seen[strings.ToLower(value)]; !ok {
			return false
		}
	}
	return true
}

// normalizeHostPort reduces a base URL or address to a comparable "host:port",
// dropping scheme/path and folding loopback/any-interface hosts to 127.0.0.1 so
// a scanned endpoint can be matched against an AIMA deployment address.
func normalizeHostPort(addr string) string {
	addr = strings.TrimSpace(strings.ToLower(addr))
	if addr == "" {
		return ""
	}
	if i := strings.Index(addr, "://"); i >= 0 {
		addr = addr[i+3:]
	}
	if i := strings.IndexByte(addr, '/'); i >= 0 {
		addr = addr[:i]
	}
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return addr
	}
	switch host {
	case "localhost", "::1", "[::1]", "0.0.0.0", "":
		host = "127.0.0.1"
	}
	return host + ":" + port
}

// ownDeploymentAddrs returns the set of host:port addresses backing AIMA's own
// (non-external) proxy deployments. The external scanner probes llama.cpp's
// default port 8080, which an AIMA native deployment also binds, so these must
// be excluded to avoid surfacing AIMA's own backend as an importable service.
func (r *Reconciler) ownDeploymentAddrs() map[string]struct{} {
	own := make(map[string]struct{})
	if r.proxy == nil {
		return own
	}
	for _, b := range r.proxy.ListBackends() {
		if b == nil || b.External {
			continue
		}
		if a := normalizeHostPort(b.Address); a != "" {
			own[a] = struct{}{}
		}
	}
	return own
}

// filterOutOwnDeployments drops services whose address matches an AIMA-owned
// deployment backend.
func filterOutOwnDeployments(services []Overview, own map[string]struct{}) []Overview {
	if len(own) == 0 {
		return services
	}
	out := make([]Overview, 0, len(services))
	for _, svc := range services {
		if _, ok := own[normalizeHostPort(svc.BaseURL)]; ok {
			continue
		}
		out = append(out, svc)
	}
	return out
}

// staleScannedAddrs returns base URLs of auto-discovered (scanned, non-imported)
// services that were not seen in the latest scan and are not AIMA's own
// deployments — i.e. services that have disappeared (e.g. a model that was
// undeployed) and should be marked unreachable so they drop out of the list.
func staleScannedAddrs(existing []*state.ExternalService, reachable, own map[string]struct{}) []string {
	var stale []string
	for _, ex := range existing {
		if ex == nil || ex.Imported || ex.Source != "scan" {
			continue
		}
		if strings.EqualFold(ex.Status, "unreachable") {
			continue
		}
		if _, ok := reachable[ex.BaseURL]; ok {
			continue
		}
		if _, ok := own[normalizeHostPort(ex.BaseURL)]; ok {
			continue
		}
		stale = append(stale, ex.BaseURL)
	}
	return stale
}
