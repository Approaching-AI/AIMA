package support

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	ConfigEnabled    = "support.enabled"
	ConfigEndpoint   = "support.endpoint"
	ConfigInviteCode = "support.invite_code"
	ConfigWorkerCode = "support.worker_code"

	DefaultEndpoint   = "https://aimaserver.com/platform"
	DefaultInviteCode = "channel-aima"

	configStateDeviceID             = "support.state.device_id"
	configStateToken                = "support.state.token"
	configStateRecoveryCode         = "support.state.recovery_code"
	configStateReferralCode         = "support.state.referral_code"
	configStateShareText            = "support.state.share_text"
	configStateTokenExpiresAt       = "support.state.token_expires_at"
	configStatePollIntervalSec      = "support.state.poll_interval_seconds"
	configStateMaxTasks             = "support.state.max_tasks"
	configStateUsedTasks            = "support.state.used_tasks"
	configStateBudgetUSD            = "support.state.budget_usd"
	configStateSpentUSD             = "support.state.spent_usd"
	configStateBudgetStatus         = "support.state.budget_status"
	configStateIsBound              = "support.state.is_bound"
	configStateReferralCount        = "support.state.referral_count"
	configStateActiveTaskID         = "support.state.active_task_id"
	configStateActiveTaskStatus     = "support.state.active_task_status"
	configStateActiveTaskTarget     = "support.state.active_task_target"
	configStateActiveTaskUpdatedAt  = "support.state.active_task_updated_at"
	configStateLastTaskID           = "support.state.last_task_id"
	configStateLastTaskStatus       = "support.state.last_task_status"
	configStateLastTaskUpdatedAt    = "support.state.last_task_updated_at"
	configStateLastMessage          = "support.state.last_message"
	configStateLastMessageType      = "support.state.last_message_type"
	configStateLastMessageLevel     = "support.state.last_message_level"
	configStateLastMessagePhase     = "support.state.last_message_phase"
	configStateLastMessageUpdatedAt = "support.state.last_message_updated_at"

	defaultPollInterval     = 5 * time.Second
	defaultProgressInterval = 5 * time.Second
	defaultDisabledRetry    = 5 * time.Second
	defaultPreviewLimit     = 16 * 1024
	defaultOutputLimit      = 512 * 1024
)

// ConfigStore persists support settings and device state.
type ConfigStore interface {
	GetConfig(ctx context.Context, key string) (string, error)
	SetConfig(ctx context.Context, key, value string) error
}

// Prompt carries a pending interaction from the remote support service.
type Prompt struct {
	InteractionID string
	Question      string
	Type          string
	Level         string
	Phase         string
}

// Notification reports support-side status updates and task completion.
type Notification struct {
	Message              string
	Type                 string
	Level                string
	Phase                string
	TaskID               string
	TaskStatus           string
	ReferralCode         string
	ShareText            string
	BudgetTasksRemaining int
	BudgetTasksTotal     int
	BudgetUSDRemaining   float64
	BudgetUSDTotal       float64
}

// PromptFunc answers interactive prompts from the support service.
type PromptFunc func(ctx context.Context, prompt Prompt) (string, error)

// NotifyFunc receives background notifications from the support service.
type NotifyFunc func(ctx context.Context, notification Notification)

// AskRequest captures the user-facing support entrypoint parameters.
type AskRequest struct {
	Description  string
	Endpoint     string
	InviteCode   string
	WorkerCode   string
	RecoveryCode string
	ReferralCode string
}

// AskResult is returned by CLI, MCP, and UI support entrypoints.
type AskResult struct {
	Enabled             bool    `json:"enabled"`
	Endpoint            string  `json:"endpoint"`
	DeviceID            string  `json:"device_id"`
	PollIntervalSeconds int     `json:"poll_interval_seconds,omitempty"`
	Created             bool    `json:"created"`
	ReusedActiveTask    bool    `json:"reused_active_task"`
	TaskID              string  `json:"task_id,omitempty"`
	TaskStatus          string  `json:"task_status,omitempty"`
	TaskTarget          string  `json:"task_target,omitempty"`
	ReferralCode        string  `json:"referral_code,omitempty"`
	ShareText           string  `json:"share_text,omitempty"`
	MaxTasks            int     `json:"max_tasks,omitempty"`
	UsedTasks           int     `json:"used_tasks,omitempty"`
	BudgetUSD           float64 `json:"budget_usd,omitempty"`
	SpentUSD            float64 `json:"spent_usd,omitempty"`
	BudgetStatus        string  `json:"budget_status,omitempty"`
	IsBound             bool    `json:"is_bound,omitempty"`
	ReferralCount       int     `json:"referral_count,omitempty"`
}

// TaskSnapshot captures the latest persisted support task state.
type TaskSnapshot struct {
	TaskID    string `json:"task_id,omitempty"`
	Status    string `json:"status,omitempty"`
	Target    string `json:"target,omitempty"`
	UpdatedAt string `json:"updated_at,omitempty"`
}

// MessageSnapshot captures the latest persisted support interaction message.
type MessageSnapshot struct {
	Seq       int64  `json:"seq,omitempty"`
	Message   string `json:"message,omitempty"`
	Type      string `json:"type,omitempty"`
	Level     string `json:"level,omitempty"`
	Phase     string `json:"phase,omitempty"`
	UpdatedAt string `json:"updated_at,omitempty"`
}

// Status is the persisted support state exposed to other AIMA surfaces like the UI.
type Status struct {
	Enabled             bool              `json:"enabled"`
	Endpoint            string            `json:"endpoint,omitempty"`
	Registered          bool              `json:"registered"`
	DeviceID            string            `json:"device_id,omitempty"`
	ReferralCode        string            `json:"referral_code,omitempty"`
	ShareText           string            `json:"share_text,omitempty"`
	PollIntervalSeconds int               `json:"poll_interval_seconds,omitempty"`
	MaxTasks            int               `json:"max_tasks,omitempty"`
	UsedTasks           int               `json:"used_tasks,omitempty"`
	BudgetUSD           float64           `json:"budget_usd,omitempty"`
	SpentUSD            float64           `json:"spent_usd,omitempty"`
	BudgetStatus        string            `json:"budget_status,omitempty"`
	IsBound             bool              `json:"is_bound,omitempty"`
	ReferralCount       int               `json:"referral_count,omitempty"`
	ActiveTask          *TaskSnapshot     `json:"active_task,omitempty"`
	LastTask            *TaskSnapshot     `json:"last_task,omitempty"`
	LastMessage         *MessageSnapshot  `json:"last_message,omitempty"`
	Messages            []MessageSnapshot `json:"messages,omitempty"`
}

// RunOptions control how the support polling loop behaves.
type RunOptions struct {
	StopWhenIdle bool
	Prompt       PromptFunc
	Notify       NotifyFunc
}

// RegistrationPromptKind identifies which extra credential the platform needs.
type RegistrationPromptKind string

const (
	RegistrationPromptInviteOrWorker RegistrationPromptKind = "invite_or_worker"
	RegistrationPromptRecovery       RegistrationPromptKind = "recovery_code"
)

// RegistrationPromptError indicates registration can continue after asking the
// user for an invite/worker code or recovery code.
type RegistrationPromptError struct {
	Kind   RegistrationPromptKind
	Detail string
}

func (e *RegistrationPromptError) Error() string {
	switch e.Kind {
	case RegistrationPromptInviteOrWorker:
		if e.Detail != "" {
			return fmt.Sprintf("support registration needs invite or worker code: %s", e.Detail)
		}
		return "support registration needs invite or worker code"
	case RegistrationPromptRecovery:
		if e.Detail != "" {
			return fmt.Sprintf("support registration needs recovery code: %s", e.Detail)
		}
		return "support registration needs recovery code"
	default:
		if e.Detail != "" {
			return e.Detail
		}
		return "support registration needs more input"
	}
}

// Option customizes a Service.
type Option func(*Service)

const maxMessageLog = 100

// Service is the built-in AIMA device client for aima-service-new.
type Service struct {
	store            ConfigStore
	client           *http.Client
	logger           *slog.Logger
	now              func() time.Time
	progressInterval time.Duration
	outputLimit      int
	previewLimit     int

	msgMu  sync.Mutex
	msgLog []MessageSnapshot
	msgSeq int64
}

// NewService constructs a support client backed by the given config store.
func NewService(store ConfigStore, opts ...Option) *Service {
	s := &Service{
		store:  store,
		client: &http.Client{Timeout: 30 * time.Second},
		logger: slog.Default(),
		now:    time.Now,

		progressInterval: defaultProgressInterval,
		outputLimit:      defaultOutputLimit,
		previewLimit:     defaultPreviewLimit,
	}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

// WithHTTPClient overrides the HTTP client used to talk to the support service.
func WithHTTPClient(client *http.Client) Option {
	return func(s *Service) {
		if client != nil {
			s.client = client
		}
	}
}

// WithLogger overrides the logger used by the support client.
func WithLogger(logger *slog.Logger) Option {
	return func(s *Service) {
		if logger != nil {
			s.logger = logger
		}
	}
}

// WithProgressInterval overrides how often long-running commands send progress.
func WithProgressInterval(interval time.Duration) Option {
	return func(s *Service) {
		if interval > 0 {
			s.progressInterval = interval
		}
	}
}

// AskForHelpJSON is the MCP/CLI entrypoint — performs AskForHelp and returns
// the result as a json.RawMessage ready for tool responses.
func (s *Service) AskForHelpJSON(ctx context.Context, description, endpoint, inviteCode, workerCode, recoveryCode, referralCode string) (json.RawMessage, error) {
	result, err := s.AskForHelp(ctx, AskRequest{
		Description:  description,
		Endpoint:     endpoint,
		InviteCode:   inviteCode,
		WorkerCode:   workerCode,
		RecoveryCode: recoveryCode,
		ReferralCode: referralCode,
	})
	if err != nil {
		return nil, err
	}
	return json.Marshal(result)
}

// AskForHelp ensures this AIMA instance is registered as a support device and
// optionally creates a new remote help task.
func (s *Service) AskForHelp(ctx context.Context, req AskRequest) (AskResult, error) {
	if err := s.persistOverrides(ctx, req); err != nil {
		return AskResult{}, err
	}

	state, endpoint, registerResp, err := s.ensureRegistered(ctx, req)
	if err != nil {
		return AskResult{}, err
	}
	if err := s.store.SetConfig(ctx, ConfigEnabled, "true"); err != nil {
		return AskResult{}, fmt.Errorf("enable support: %w", err)
	}

	active, err := s.getActiveTask(ctx, endpoint, state)
	if err != nil {
		return AskResult{}, err
	}
	s.persistActiveTask(ctx, active.TaskID, active.Status, active.Target)

	result := AskResult{
		Enabled:             true,
		Endpoint:            endpoint,
		DeviceID:            state.DeviceID,
		PollIntervalSeconds: state.PollIntervalSeconds,
		ReferralCode:        state.ReferralCode,
		ShareText:           state.ShareText,
		MaxTasks:            state.MaxTasks,
		UsedTasks:           state.UsedTasks,
		BudgetUSD:           state.BudgetUSD,
		SpentUSD:            state.SpentUSD,
		BudgetStatus:        state.BudgetStatus,
		IsBound:             state.IsBound,
		ReferralCount:       state.ReferralCount,
	}
	if registerResp != nil && result.ReferralCode == "" {
		result.ReferralCode = registerResp.ReferralCode
	}

	if req.Description == "" {
		if active.HasActiveTask {
			result.TaskID = active.TaskID
			result.TaskStatus = active.Status
			result.TaskTarget = active.Target
			result.ReusedActiveTask = true
		}
		return result, nil
	}

	if active.HasActiveTask {
		result.TaskID = active.TaskID
		result.TaskStatus = active.Status
		result.TaskTarget = active.Target
		result.ReusedActiveTask = true
		return result, nil
	}

	task, err := s.createTask(ctx, endpoint, state, req.Description)
	if err != nil {
		var statusErr *httpStatusError
		if errors.As(err, &statusErr) && statusErr.StatusCode == http.StatusConflict {
			active, activeErr := s.getActiveTask(ctx, endpoint, state)
			if activeErr != nil {
				return AskResult{}, activeErr
			}
			s.persistActiveTask(ctx, active.TaskID, active.Status, active.Target)
			result.TaskID = active.TaskID
			result.TaskStatus = active.Status
			result.TaskTarget = active.Target
			result.ReusedActiveTask = true
			return result, nil
		}
		return AskResult{}, err
	}

	s.persistActiveTask(ctx, task.TaskID, task.Status, req.Description)
	result.Created = true
	result.TaskID = task.TaskID
	result.TaskStatus = task.Status
	result.TaskTarget = strings.TrimSpace(req.Description)
	return result, nil
}

// GoUXManifestJSON returns the current aima-service-new device-go UX manifest.
// This is the shared UX source consumed by bootstrap scripts and the Python CLI.
func (s *Service) GoUXManifestJSON(ctx context.Context) (json.RawMessage, error) {
	endpoint := s.endpointFromConfig(ctx)
	if endpoint == "" {
		return nil, fmt.Errorf("support endpoint is not configured; set %s or AIMA_SUPPORT_ENDPOINT", ConfigEndpoint)
	}

	query := url.Values{}
	query.Set("schema_version", "v1")
	if worker := s.optionalConfig(ctx, ConfigWorkerCode, "AIMA_SUPPORT_WORKER_CODE"); worker != "" {
		query.Set("worker_code", worker)
	}
	if state := s.loadState(ctx); state.ReferralCode != "" {
		query.Set("ref", state.ReferralCode)
	}

	var raw json.RawMessage
	manifestURL := endpoint + "/ux-manifests/device-go"
	if encoded := query.Encode(); encoded != "" {
		manifestURL += "?" + encoded
	}
	if err := s.doJSON(ctx, http.MethodGet, manifestURL, "", nil, &raw); err != nil {
		return nil, err
	}
	return raw, nil
}

// Status returns the latest persisted support state without performing network I/O.
func (s *Service) Status(ctx context.Context) Status {
	state := s.loadState(ctx)
	status := Status{
		Enabled:             s.isEnabled(ctx),
		Endpoint:            s.endpointFromConfig(ctx),
		Registered:          state.DeviceID != "",
		DeviceID:            state.DeviceID,
		ReferralCode:        state.ReferralCode,
		ShareText:           state.ShareText,
		PollIntervalSeconds: state.PollIntervalSeconds,
		MaxTasks:            state.MaxTasks,
		UsedTasks:           state.UsedTasks,
		BudgetUSD:           state.BudgetUSD,
		SpentUSD:            state.SpentUSD,
		BudgetStatus:        state.BudgetStatus,
		IsBound:             state.IsBound,
		ReferralCount:       state.ReferralCount,
	}

	if active := s.loadTaskSnapshot(ctx, configStateActiveTaskID, configStateActiveTaskStatus, configStateActiveTaskTarget, configStateActiveTaskUpdatedAt); hasTaskSnapshot(active) {
		status.ActiveTask = &active
	}
	if last := s.loadTaskSnapshot(ctx, configStateLastTaskID, configStateLastTaskStatus, "", configStateLastTaskUpdatedAt); hasTaskSnapshot(last) {
		status.LastTask = &last
	}
	if message := s.loadMessageSnapshot(ctx); hasMessageSnapshot(message) {
		status.LastMessage = &message
	}
	status.Messages = s.MessagesSince(0)
	return status
}

// RunBackground keeps a support polling loop alive for as long as ctx is active.
// It is safe to call from `aima serve`; the loop idles until support is enabled.
func (s *Service) RunBackground(ctx context.Context) error {
	err := s.Run(ctx, RunOptions{})
	if errors.Is(err, context.Canceled) {
		return nil
	}
	return err
}

// Run executes the support polling loop. With StopWhenIdle=true, the loop exits
// after the current active task finishes.
func (s *Service) Run(ctx context.Context, opts RunOptions) error {
	var sawActive bool
	for {
		if err := ctx.Err(); err != nil {
			return err
		}

		enabled := s.isEnabled(ctx)
		endpoint := s.endpointFromConfig(ctx)
		if !enabled || endpoint == "" {
			if opts.StopWhenIdle {
				return nil
			}
			if err := sleepContext(ctx, defaultDisabledRetry); err != nil {
				return err
			}
			continue
		}

		state, endpoint, _, err := s.ensureRegistered(ctx, AskRequest{})
		if err != nil {
			if opts.StopWhenIdle {
				return err
			}
			s.logger.Warn("support registration failed", "error", err)
			if err := sleepContext(ctx, defaultDisabledRetry); err != nil {
				return err
			}
			continue
		}

		state, err = s.renewTokenIfNeeded(ctx, endpoint, state)
		if err != nil {
			if isAuthError(err) {
				s.logger.Warn("support token rejected; re-registering", "error", err)
				_ = s.clearSavedToken(ctx)
			} else if isTransientError(err) {
				if err := s.retryTransient(ctx, "renew_token", err, defaultPollInterval); err != nil {
					return err
				}
				continue
			} else if opts.StopWhenIdle {
				return err
			} else {
				s.logger.Warn("support token renewal failed", "error", err)
			}
		}

		pollResp, err := s.poll(ctx, endpoint, state, 20)
		if err != nil {
			if isAuthError(err) {
				s.logger.Warn("support poll rejected; re-registering", "error", err)
				_ = s.clearSavedToken(ctx)
				continue
			}
			if isTransientError(err) {
				if err := s.retryTransient(ctx, "poll", err, defaultPollInterval); err != nil {
					return err
				}
				continue
			}
			if opts.StopWhenIdle {
				return err
			}
			s.logger.Warn("support poll failed", "error", err)
			if err := sleepContext(ctx, defaultPollInterval); err != nil {
				return err
			}
			continue
		}

		if pollResp.PollIntervalSeconds > 0 {
			state.PollIntervalSeconds = pollResp.PollIntervalSeconds
			_ = s.store.SetConfig(ctx, configStatePollIntervalSec, fmt.Sprintf("%d", pollResp.PollIntervalSeconds))
		}
		if pollResp.IsBound != state.IsBound {
			state.IsBound = pollResp.IsBound
			s.persistStateEntries(ctx, map[string]string{
				configStateIsBound: strconv.FormatBool(state.IsBound),
			})
		}

		if pollResp.InteractionID != "" {
			sawActive = true
			if err := s.handleInteraction(ctx, endpoint, state, pollResp, opts); err != nil {
				if isTransientError(err) {
					if err := s.retryTransient(ctx, "interaction", err, 3*time.Second); err != nil {
						return err
					}
					continue
				}
				if opts.StopWhenIdle {
					return err
				}
				s.logger.Warn("support interaction failed", "interaction_id", pollResp.InteractionID, "error", err)
				if err := sleepContext(ctx, 3*time.Second); err != nil {
					return err
				}
			}
			continue
		}

		if pollResp.CommandID != "" && pollResp.Command != "" {
			sawActive = true
			if err := s.executeCommands(ctx, endpoint, state, pollResp); err != nil {
				if isAuthError(err) {
					s.logger.Warn("support command submission rejected; re-registering", "error", err)
					_ = s.clearSavedToken(ctx)
					continue
				}
				if isTransientError(err) {
					if err := s.retryTransient(ctx, "command_execution", err, defaultPollInterval); err != nil {
						return err
					}
					continue
				}
				if opts.StopWhenIdle {
					return err
				}
				s.logger.Warn("support command execution failed", "command_id", pollResp.CommandID, "error", err)
			}
			continue
		}

		if pollResp.NotifTaskID != "" || pollResp.NotifTaskStatus != "" {
			sawActive = true
			msg := pollResp.NotifTaskMessage
			if msg == "" {
				msg = fmt.Sprintf("Task %s finished with status %s", pollResp.NotifTaskID, pollResp.NotifTaskStatus)
			}
			notification := Notification{
				Message:              msg,
				Type:                 "task_completion",
				TaskID:               pollResp.NotifTaskID,
				TaskStatus:           pollResp.NotifTaskStatus,
				ReferralCode:         pollResp.NotifReferralCode,
				ShareText:            pollResp.NotifShareText,
				BudgetTasksRemaining: pollResp.NotifBudgetTasksRemaining,
				BudgetTasksTotal:     pollResp.NotifBudgetTasksTotal,
				BudgetUSDRemaining:   pollResp.NotifBudgetUSDRemaining,
				BudgetUSDTotal:       pollResp.NotifBudgetUSDTotal,
			}
			s.persistNotification(ctx, notification)
			s.emitNotification(ctx, opts.Notify, notification)
		}

		active, err := s.getActiveTask(ctx, endpoint, state)
		if err != nil {
			if isAuthError(err) {
				_ = s.clearSavedToken(ctx)
				continue
			}
			if isTransientError(err) {
				if err := s.retryTransient(ctx, "active_task", err, defaultPollInterval); err != nil {
					return err
				}
				continue
			}
			if opts.StopWhenIdle {
				return err
			}
			s.logger.Warn("support active-task check failed", "error", err)
			if err := sleepContext(ctx, defaultPollInterval); err != nil {
				return err
			}
			continue
		}
		s.persistActiveTask(ctx, active.TaskID, active.Status, active.Target)
		if active.HasActiveTask {
			sawActive = true
		} else if opts.StopWhenIdle && sawActive {
			return nil
		}

		if err := sleepContext(ctx, nextPollInterval(state.PollIntervalSeconds)); err != nil {
			return err
		}
	}
}

type deviceState struct {
	DeviceID            string
	Token               string
	RecoveryCode        string
	ReferralCode        string
	ShareText           string
	TokenExpiresAt      string
	PollIntervalSeconds int
	MaxTasks            int
	UsedTasks           int
	BudgetUSD           float64
	SpentUSD            float64
	BudgetStatus        string
	IsBound             bool
	ReferralCount       int
}

type activeTaskResponse struct {
	HasActiveTask bool   `json:"has_active_task"`
	TaskID        string `json:"task_id"`
	Status        string `json:"status"`
	Target        string `json:"target"`
}

type deviceTaskResponse struct {
	TaskID string `json:"task_id"`
	Status string `json:"status"`
}

type selfRegisterResponse struct {
	DeviceID            string     `json:"device_id"`
	Token               string     `json:"token"`
	RecoveryCode        string     `json:"recovery_code"`
	TokenExpiresAt      string     `json:"token_expires_at"`
	PollIntervalSeconds int        `json:"poll_interval_seconds"`
	Budget              budgetInfo `json:"budget"`
	ReferralCode        string     `json:"referral_code"`
	ShareText           string     `json:"share_text"`
	DisplayLanguage     string     `json:"display_language,omitempty"`
}

type budgetInfo struct {
	MaxTasks      int     `json:"max_tasks"`
	UsedTasks     int     `json:"used_tasks"`
	BudgetUSD     float64 `json:"budget_usd"`
	SpentUSD      float64 `json:"spent_usd"`
	Status        string  `json:"status"`
	IsBound       bool    `json:"is_bound"`
	ReferralCode  string  `json:"referral_code,omitempty"`
	ReferralCount int     `json:"referral_count"`
}

type renewTokenResponse struct {
	Token          string `json:"token"`
	TokenExpiresAt string `json:"token_expires_at"`
}

type pollResponse struct {
	CommandID                 string  `json:"command_id"`
	Command                   string  `json:"command"`
	CommandEncoding           string  `json:"command_encoding"`
	CommandTimeoutSeconds     int     `json:"command_timeout_seconds"`
	CommandIntent             string  `json:"command_intent"`
	InteractionID             string  `json:"interaction_id"`
	Question                  string  `json:"question"`
	InteractionType           string  `json:"interaction_type"`
	InteractionLevel          string  `json:"interaction_level"`
	InteractionPhase          string  `json:"interaction_phase"`
	PollIntervalSeconds       int     `json:"poll_interval_seconds"`
	IsBound                   bool    `json:"is_bound"`
	NotifTaskID               string  `json:"notif_task_id"`
	NotifTaskStatus           string  `json:"notif_task_status"`
	NotifTaskMessage          string  `json:"notif_task_message"`
	NotifReferralCode         string  `json:"notif_referral_code"`
	NotifShareText            string  `json:"notif_share_text"`
	NotifBudgetTasksRemaining int     `json:"notif_budget_tasks_remaining"`
	NotifBudgetTasksTotal     int     `json:"notif_budget_tasks_total"`
	NotifBudgetUSDRemaining   float64 `json:"notif_budget_usd_remaining"`
	NotifBudgetUSDTotal       float64 `json:"notif_budget_usd_total"`
}

type commandProgressAckResponse struct {
	OK              bool   `json:"ok"`
	CancelRequested bool   `json:"cancel_requested"`
	CommandStatus   string `json:"command_status"`
}

type commandResultAckResponse struct {
	OK                        bool   `json:"ok"`
	NextCommandID             string `json:"next_command_id"`
	NextCommand               string `json:"next_command"`
	NextCommandEncoding       string `json:"next_command_encoding"`
	NextCommandTimeoutSeconds int    `json:"next_command_timeout_seconds"`
	NextCommandIntent         string `json:"next_command_intent"`
	PollIntervalSeconds       int    `json:"poll_interval_seconds"`
}

func (s *Service) ensureRegistered(ctx context.Context, req AskRequest) (deviceState, string, *selfRegisterResponse, error) {
	state := s.loadState(ctx)
	endpoint := s.endpointFromConfig(ctx)
	if endpoint == "" {
		return deviceState{}, "", nil, fmt.Errorf("support endpoint is not configured; set %s or AIMA_SUPPORT_ENDPOINT", ConfigEndpoint)
	}

	if state.DeviceID != "" && state.Token != "" {
		if _, err := s.getActiveTask(ctx, endpoint, state); err == nil {
			return state, endpoint, nil, nil
		} else if !isAuthError(err) {
			return deviceState{}, "", nil, err
		}
	}

	registerReq, err := buildSelfRegisterRequest(ctx)
	if err != nil {
		return deviceState{}, "", nil, err
	}
	if recovery := strings.TrimSpace(req.RecoveryCode); recovery != "" {
		state.RecoveryCode = recovery
	}
	if state.RecoveryCode != "" {
		registerReq["recovery_code"] = state.RecoveryCode
	}
	if referral := strings.TrimSpace(req.ReferralCode); referral != "" {
		registerReq["referral_code"] = referral
	}
	invite := s.optionalConfig(ctx, ConfigInviteCode, "AIMA_SUPPORT_INVITE_CODE")
	if invite == "" {
		invite = DefaultInviteCode
	}
	registerReq["invite_code"] = invite
	if worker := s.optionalConfig(ctx, ConfigWorkerCode, "AIMA_SUPPORT_WORKER_CODE"); worker != "" {
		registerReq["worker_enrollment_code"] = worker
	}

	var resp selfRegisterResponse
	if err := s.doJSON(ctx, http.MethodPost, endpoint+"/devices/self-register", "", registerReq, &resp); err != nil {
		return deviceState{}, "", nil, classifyRegistrationError(err)
	}
	state.DeviceID = resp.DeviceID
	state.Token = resp.Token
	state.RecoveryCode = resp.RecoveryCode
	state.ReferralCode = resp.ReferralCode
	state.TokenExpiresAt = resp.TokenExpiresAt
	if resp.PollIntervalSeconds > 0 {
		state.PollIntervalSeconds = resp.PollIntervalSeconds
	}
	applyRegistrationSummary(&state, &resp)
	if err := s.saveState(ctx, state); err != nil {
		return deviceState{}, "", nil, err
	}
	return state, endpoint, &resp, nil
}

func (s *Service) renewTokenIfNeeded(ctx context.Context, endpoint string, state deviceState) (deviceState, error) {
	if state.DeviceID == "" || state.Token == "" {
		return state, nil
	}
	if state.TokenExpiresAt != "" {
		if expiresAt, err := time.Parse(time.RFC3339, state.TokenExpiresAt); err == nil {
			if time.Until(expiresAt) > 24*time.Hour {
				return state, nil
			}
		}
	}

	var resp renewTokenResponse
	err := s.doJSON(ctx, http.MethodPost, endpoint+"/devices/"+state.DeviceID+"/renew-token", state.Token, nil, &resp)
	if err != nil {
		return state, err
	}
	if resp.Token != "" {
		state.Token = resp.Token
	}
	if resp.TokenExpiresAt != "" {
		state.TokenExpiresAt = resp.TokenExpiresAt
	}
	if err := s.saveState(ctx, state); err != nil {
		return state, err
	}
	return state, nil
}

func (s *Service) poll(ctx context.Context, endpoint string, state deviceState, waitSeconds int) (pollResponse, error) {
	var resp pollResponse
	url := fmt.Sprintf("%s/devices/%s/poll?wait=%d", endpoint, state.DeviceID, waitSeconds)
	if err := s.doJSON(ctx, http.MethodGet, url, state.Token, nil, &resp); err != nil {
		return pollResponse{}, err
	}
	return resp, nil
}

func (s *Service) getActiveTask(ctx context.Context, endpoint string, state deviceState) (activeTaskResponse, error) {
	var resp activeTaskResponse
	if err := s.doJSON(ctx, http.MethodGet, endpoint+"/devices/"+state.DeviceID+"/active-task", state.Token, nil, &resp); err != nil {
		return activeTaskResponse{}, err
	}
	return resp, nil
}

func (s *Service) createTask(ctx context.Context, endpoint string, state deviceState, description string) (deviceTaskResponse, error) {
	var resp deviceTaskResponse
	body := map[string]any{"description": description}
	if err := s.doJSON(ctx, http.MethodPost, endpoint+"/devices/"+state.DeviceID+"/tasks", state.Token, body, &resp); err != nil {
		return deviceTaskResponse{}, err
	}
	return resp, nil
}

func (s *Service) handleInteraction(ctx context.Context, endpoint string, state deviceState, resp pollResponse, opts RunOptions) error {
	notification := Notification{
		Message: resp.Question,
		Type:    resp.InteractionType,
		Level:   resp.InteractionLevel,
		Phase:   resp.InteractionPhase,
	}
	s.persistNotification(ctx, notification)
	if resp.InteractionType == "notification" {
		s.emitNotification(ctx, opts.Notify, notification)
		return s.respondInteraction(ctx, endpoint, state, resp.InteractionID, "displayed")
	}

	answer := defaultInteractionAnswer(resp)
	if opts.Prompt != nil {
		prompt := Prompt{
			InteractionID: resp.InteractionID,
			Question:      resp.Question,
			Type:          resp.InteractionType,
			Level:         resp.InteractionLevel,
			Phase:         resp.InteractionPhase,
		}
		if response, err := opts.Prompt(ctx, prompt); err == nil && strings.TrimSpace(response) != "" {
			answer = strings.TrimSpace(response)
		} else if err != nil {
			s.logger.Warn("support prompt handler failed; using auto-answer", "interaction_id", resp.InteractionID, "error", err)
		}
	}
	return s.respondInteraction(ctx, endpoint, state, resp.InteractionID, answer)
}

func (s *Service) respondInteraction(ctx context.Context, endpoint string, state deviceState, interactionID, answer string) error {
	body := map[string]any{"answer": answer}
	return s.doJSON(ctx, http.MethodPost, endpoint+"/devices/"+state.DeviceID+"/interactions/"+interactionID+"/respond", state.Token, body, nil)
}
