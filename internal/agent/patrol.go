package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"
)

// PatrolConfig controls the patrol loop behavior.
type PatrolConfig struct {
	Interval           time.Duration
	GPUTempWarnC       int // GPU temp threshold (default 85)
	GPUIdlePct         int // below this = idle (default 10)
	GPUIdleMinutes     int // idle for this long = alert (default 15)
	VRAMOpportunityPct int // above this free = opportunity (default 50)
	SelfHealEnabled    bool
}

// DefaultPatrolConfig returns sensible defaults.
func DefaultPatrolConfig() PatrolConfig {
	return PatrolConfig{
		Interval:           5 * time.Minute,
		GPUTempWarnC:       85,
		GPUIdlePct:         10,
		GPUIdleMinutes:     15,
		VRAMOpportunityPct: 50,
		SelfHealEnabled:    true,
	}
}

// Alert is a structured patrol alert.
type Alert struct {
	ID         string    `json:"id"`
	Severity   string    `json:"severity"` // "info", "warning", "critical"
	Type       string    `json:"type"`     // "gpu_temp", "gpu_idle", "deploy_crash", "vram_opportunity", "power_throttle"
	Message    string    `json:"message"`
	CreatedAt  time.Time `json:"created_at"`
	ResolvedAt *time.Time `json:"resolved_at,omitempty"`
	Resolved   bool      `json:"resolved"`
}

// PatrolStatus describes the patrol loop state.
type PatrolStatus struct {
	Running     bool      `json:"running"`
	LastRun     time.Time `json:"last_run,omitempty"`
	NextRun     time.Time `json:"next_run,omitempty"`
	AlertCount  int       `json:"alert_count"`
	ActionCount int       `json:"action_count"`
	HealCount   int       `json:"heal_count"`
	Interval    string    `json:"interval"`
}

// AlertPersister saves patrol alerts to storage.
type AlertPersister func(ctx context.Context, id, severity, typ, message string) error

// PatrolAction records an automated response to an alert.
type PatrolAction struct {
	AlertID   string    `json:"alert_id"`
	Type      string    `json:"type"`    // "heal", "notify"
	Detail    string    `json:"detail"`
	Success   bool      `json:"success"`
	Timestamp time.Time `json:"timestamp"`
}

// PatrolOption configures optional Patrol dependencies.
type PatrolOption func(*Patrol)

// WithHealer enables automated self-healing in response to critical alerts.
func WithHealer(h *Healer) PatrolOption {
	return func(p *Patrol) { p.healer = h }
}

// WithActionCallback registers a function called after each automated action.
func WithActionCallback(fn func(ctx context.Context, action PatrolAction)) PatrolOption {
	return func(p *Patrol) { p.onAction = fn }
}

// Patrol runs periodic device inspections.
type Patrol struct {
	config   PatrolConfig
	tools    ToolExecutor
	persist  AlertPersister
	healer   *Healer
	onAction func(ctx context.Context, action PatrolAction)
	mu       sync.RWMutex
	alerts   []Alert
	actions  []PatrolAction
	lastRun  time.Time
	running  bool
	cancel   context.CancelFunc
}

// NewPatrol creates a patrol loop. persist may be nil (alerts only kept in memory).
func NewPatrol(config PatrolConfig, tools ToolExecutor, persist AlertPersister, opts ...PatrolOption) *Patrol {
	p := &Patrol{
		config:  config,
		tools:   tools,
		persist: persist,
	}
	for _, o := range opts {
		o(p)
	}
	return p
}

// Start begins the background patrol loop.
func (p *Patrol) Start(ctx context.Context) {
	p.mu.Lock()
	if p.running {
		p.mu.Unlock()
		return
	}
	p.running = true
	ctx, p.cancel = context.WithCancel(ctx)
	p.mu.Unlock()

	go func() {
		ticker := time.NewTicker(p.config.Interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				alerts := p.RunOnce(ctx)
				if len(alerts) > 0 {
					slog.Info("patrol cycle generated alerts", "count", len(alerts))
				}
			}
		}
	}()
}

// Stop cancels the patrol loop.
func (p *Patrol) Stop() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.cancel != nil {
		p.cancel()
	}
	p.running = false
}

// Status returns current patrol state.
func (p *Patrol) Status() PatrolStatus {
	p.mu.RLock()
	defer p.mu.RUnlock()
	healCount := 0
	for _, a := range p.actions {
		if a.Type == "heal" && a.Success {
			healCount++
		}
	}
	s := PatrolStatus{
		Running:     p.running,
		LastRun:     p.lastRun,
		AlertCount:  len(p.alerts),
		ActionCount: len(p.actions),
		HealCount:   healCount,
		Interval:    p.config.Interval.String(),
	}
	if p.running && !p.lastRun.IsZero() {
		s.NextRun = p.lastRun.Add(p.config.Interval)
	}
	return s
}

// ActiveAlerts returns unresolved alerts.
func (p *Patrol) ActiveAlerts() []Alert {
	p.mu.RLock()
	defer p.mu.RUnlock()
	var active []Alert
	for _, a := range p.alerts {
		if !a.Resolved {
			active = append(active, a)
		}
	}
	return active
}

// RunOnce performs a single patrol cycle.
func (p *Patrol) RunOnce(ctx context.Context) []Alert {
	var newAlerts []Alert

	// 1. Device metrics check
	metricsAlerts := p.checkMetrics(ctx)
	newAlerts = append(newAlerts, metricsAlerts...)

	// 2. Deployment health check
	deployAlerts := p.checkDeployments(ctx)
	newAlerts = append(newAlerts, deployAlerts...)

	// 3. Persist and track
	p.mu.Lock()
	p.alerts = append(p.alerts, newAlerts...)
	p.lastRun = time.Now()
	p.mu.Unlock()

	if p.persist != nil {
		for _, alert := range newAlerts {
			if err := p.persist(ctx, alert.ID, alert.Severity, alert.Type, alert.Message); err != nil {
				slog.Warn("patrol: failed to persist alert", "error", err)
			}
		}
	}

	// Automated response to alerts
	if p.config.SelfHealEnabled {
		p.reactToAlerts(ctx, newAlerts)
	}

	return newAlerts
}

func (p *Patrol) checkMetrics(ctx context.Context) []Alert {
	result, err := p.tools.ExecuteTool(ctx, "device.metrics", nil)
	if err != nil {
		slog.Debug("patrol: device.metrics unavailable", "error", err)
		return nil
	}

	var metrics struct {
		GPU *struct {
			TemperatureCelsius float64 `json:"temperature_celsius"`
			UtilizationPercent int     `json:"utilization_percent"`
			MemoryUsedMiB      int     `json:"memory_used_mib"`
			MemoryTotalMiB     int     `json:"memory_total_mib"`
			PowerDrawWatts     float64 `json:"power_draw_watts"`
		} `json:"gpu"`
	}
	if err := json.Unmarshal([]byte(result.Content), &metrics); err != nil || metrics.GPU == nil {
		return nil
	}

	var alerts []Alert
	gpu := metrics.GPU

	// GPU temperature
	if gpu.TemperatureCelsius > float64(p.config.GPUTempWarnC) {
		alerts = append(alerts, makeAlert("warning", "gpu_temp",
			fmt.Sprintf("GPU temperature %.0f°C exceeds threshold %d°C", gpu.TemperatureCelsius, p.config.GPUTempWarnC)))
	}

	// GPU idle
	if gpu.UtilizationPercent < p.config.GPUIdlePct {
		alerts = append(alerts, makeAlert("info", "gpu_idle",
			fmt.Sprintf("GPU utilization %d%% is below idle threshold %d%%", gpu.UtilizationPercent, p.config.GPUIdlePct)))
	}

	// VRAM opportunity
	if gpu.MemoryTotalMiB > 0 {
		freePct := 100 * (gpu.MemoryTotalMiB - gpu.MemoryUsedMiB) / gpu.MemoryTotalMiB
		if freePct > p.config.VRAMOpportunityPct {
			alerts = append(alerts, makeAlert("info", "vram_opportunity",
				fmt.Sprintf("%d%% VRAM free (%d/%d MiB) — could run another model",
					freePct, gpu.MemoryTotalMiB-gpu.MemoryUsedMiB, gpu.MemoryTotalMiB)))
		}
	}

	return alerts
}

func (p *Patrol) checkDeployments(ctx context.Context) []Alert {
	result, err := p.tools.ExecuteTool(ctx, "deploy.list", nil)
	if err != nil {
		slog.Debug("patrol: deploy.list unavailable", "error", err)
		return nil
	}

	var deploys []struct {
		Name   string `json:"name"`
		Status string `json:"status"`
	}
	if err := json.Unmarshal([]byte(result.Content), &deploys); err != nil {
		return nil
	}

	var alerts []Alert
	for _, d := range deploys {
		switch d.Status {
		case "CrashLoopBackOff", "Error", "Failed":
			alerts = append(alerts, makeAlert("critical", "deploy_crash",
				fmt.Sprintf("Deployment %s is in %s state", d.Name, d.Status)))
		}
	}
	return alerts
}

func makeAlert(severity, typ, message string) Alert {
	h := sha256.Sum256([]byte(fmt.Sprintf("%s:%s:%d", typ, message, time.Now().Unix())))
	return Alert{
		ID:        hex.EncodeToString(h[:8]),
		Severity:  severity,
		Type:      typ,
		Message:   message,
		CreatedAt: time.Now(),
	}
}

// Config returns the current patrol config (for agent.patrol_config tool).
func (p *Patrol) Config() PatrolConfig {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.config
}

// SetInterval updates the patrol interval.
func (p *Patrol) SetInterval(d time.Duration) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.config.Interval = d
}

// RecentActions returns the most recent N patrol actions.
func (p *Patrol) RecentActions(limit int) []PatrolAction {
	p.mu.RLock()
	defer p.mu.RUnlock()
	n := len(p.actions)
	if limit <= 0 || limit > n {
		limit = n
	}
	start := n - limit
	result := make([]PatrolAction, limit)
	copy(result, p.actions[start:])
	return result
}

func (p *Patrol) reactToAlerts(ctx context.Context, alerts []Alert) {
	for _, alert := range alerts {
		switch {
		case alert.Type == "deploy_crash" && alert.Severity == "critical":
			p.handleCrash(ctx, alert)
		case alert.Type == "gpu_temp" && alert.Severity == "warning":
			p.handleOverheat(ctx, alert)
		}
	}
}

func (p *Patrol) handleCrash(ctx context.Context, alert Alert) {
	if p.healer == nil {
		p.recordAction(ctx, alert.ID, "notify", "healer not configured, alert only", false)
		return
	}

	deployName := extractDeployName(alert.Message)
	if deployName == "" {
		p.recordAction(ctx, alert.ID, "notify", "could not extract deploy name from alert", false)
		return
	}

	diag, err := p.healer.Diagnose(ctx, deployName)
	if err != nil {
		p.recordAction(ctx, alert.ID, "heal", "diagnosis failed: "+err.Error(), false)
		return
	}

	if diag.Remedy == "escalate" {
		p.recordAction(ctx, alert.ID, "notify",
			fmt.Sprintf("diagnosis: %s (%s), requires human intervention", diag.Type, diag.Cause), false)
		return
	}

	action, err := p.healer.Heal(ctx, deployName, diag)
	success := err == nil && action != nil && action.Success

	detail := fmt.Sprintf("diagnosis=%s, action=%s", diag.Type, diag.Remedy)
	if action != nil {
		detail = fmt.Sprintf("diagnosis=%s, action=%s, attempt=%d", diag.Type, action.Action, action.Attempt)
	}
	p.recordAction(ctx, alert.ID, "heal", detail, success)

	if success {
		p.resolveAlert(alert.ID)
	}
}

func (p *Patrol) handleOverheat(ctx context.Context, alert Alert) {
	p.recordAction(ctx, alert.ID, "notify", "GPU overheating detected, monitoring", true)
}

func (p *Patrol) recordAction(ctx context.Context, alertID, typ, detail string, success bool) {
	action := PatrolAction{
		AlertID:   alertID,
		Type:      typ,
		Detail:    detail,
		Success:   success,
		Timestamp: time.Now(),
	}

	p.mu.Lock()
	p.actions = append(p.actions, action)
	p.mu.Unlock()

	if p.onAction != nil {
		p.onAction(ctx, action)
	}

	slog.Info("patrol action", "alert", alertID, "type", typ, "success", success, "detail", detail)
}

func (p *Patrol) resolveAlert(id string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	now := time.Now()
	for i := range p.alerts {
		if p.alerts[i].ID == id {
			p.alerts[i].Resolved = true
			p.alerts[i].ResolvedAt = &now
			break
		}
	}
}

// extractDeployName parses "Deployment <name> is in <status> state".
func extractDeployName(message string) string {
	const prefix = "Deployment "
	const suffix = " is in "
	i := strings.Index(message, prefix)
	if i < 0 {
		return ""
	}
	rest := message[i+len(prefix):]
	j := strings.Index(rest, suffix)
	if j < 0 {
		return ""
	}
	return rest[:j]
}
