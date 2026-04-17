package support

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/jguan/aima/internal/cloud"
)

func (s *Service) loadState(ctx context.Context) deviceState {
	state := deviceState{
		PollIntervalSeconds: int(defaultPollInterval / time.Second),
	}
	state.DeviceID = s.optionalConfig(ctx, configStateDeviceID, "")
	state.Token = s.optionalConfig(ctx, configStateToken, "")
	state.RecoveryCode = s.optionalConfig(ctx, configStateRecoveryCode, "")
	state.ReferralCode = s.optionalConfig(ctx, configStateReferralCode, "")
	state.ShareText = s.optionalConfig(ctx, configStateShareText, "")
	state.TokenExpiresAt = s.optionalConfig(ctx, configStateTokenExpiresAt, "")
	if raw := s.optionalConfig(ctx, configStatePollIntervalSec, ""); raw != "" {
		var parsed int
		if _, err := fmt.Sscanf(raw, "%d", &parsed); err == nil && parsed > 0 {
			state.PollIntervalSeconds = parsed
		}
	}
	state.MaxTasks = parseConfigInt(s.optionalConfig(ctx, configStateMaxTasks, ""))
	state.UsedTasks = parseConfigInt(s.optionalConfig(ctx, configStateUsedTasks, ""))
	state.BudgetUSD = parseConfigFloat(s.optionalConfig(ctx, configStateBudgetUSD, ""))
	state.SpentUSD = parseConfigFloat(s.optionalConfig(ctx, configStateSpentUSD, ""))
	state.BudgetStatus = s.optionalConfig(ctx, configStateBudgetStatus, "")
	state.IsBound = parseConfigBool(s.optionalConfig(ctx, configStateIsBound, ""))
	state.ReferralCount = parseConfigInt(s.optionalConfig(ctx, configStateReferralCount, ""))
	return state
}

func (s *Service) saveState(ctx context.Context, state deviceState) error {
	entries := map[string]string{
		configStateDeviceID:        state.DeviceID,
		configStateToken:           state.Token,
		configStateRecoveryCode:    state.RecoveryCode,
		configStateReferralCode:    state.ReferralCode,
		configStateShareText:       state.ShareText,
		configStateTokenExpiresAt:  state.TokenExpiresAt,
		configStatePollIntervalSec: fmt.Sprintf("%d", maxInt(state.PollIntervalSeconds, int(defaultPollInterval/time.Second))),
		configStateMaxTasks:        strconv.Itoa(state.MaxTasks),
		configStateUsedTasks:       strconv.Itoa(state.UsedTasks),
		configStateBudgetUSD:       strconv.FormatFloat(state.BudgetUSD, 'f', -1, 64),
		configStateSpentUSD:        strconv.FormatFloat(state.SpentUSD, 'f', -1, 64),
		configStateBudgetStatus:    state.BudgetStatus,
		configStateIsBound:         strconv.FormatBool(state.IsBound),
		configStateReferralCount:   strconv.Itoa(state.ReferralCount),
	}
	for key, value := range entries {
		if err := s.store.SetConfig(ctx, key, value); err != nil {
			return fmt.Errorf("save support state %s: %w", key, err)
		}
	}
	return s.mirrorCanonical(ctx, state)
}

// mirrorCanonical writes the identity half of deviceState to the canonical
// device.* config keys consumed by internal/cloud and outbound Central calls.
// This keeps the rest of AIMA on one surface while the support package still
// owns its own support.state.* namespace for task/budget/message state.
func (s *Service) mirrorCanonical(ctx context.Context, state deviceState) error {
	canonical := map[string]string{
		cloud.ConfigDeviceID:       state.DeviceID,
		cloud.ConfigDeviceToken:    state.Token,
		cloud.ConfigRecoveryCode:   state.RecoveryCode,
		cloud.ConfigTokenExpiresAt: state.TokenExpiresAt,
		cloud.ConfigReferralCode:   state.ReferralCode,
	}
	// Derive registration_state from presence of device_id + token so a single
	// source of truth (support saveState) can't disagree with the canonical
	// state key. Callers that want explicit failed/pending states set the key
	// directly in bootstrap.go.
	if state.DeviceID != "" && state.Token != "" {
		canonical[cloud.ConfigRegistrationState] = cloud.StateRegistered
	}
	for key, value := range canonical {
		if err := s.store.SetConfig(ctx, key, value); err != nil {
			return fmt.Errorf("mirror canonical %s: %w", key, err)
		}
	}
	return nil
}

func (s *Service) clearSavedToken(ctx context.Context) error {
	if err := s.store.SetConfig(ctx, configStateToken, ""); err != nil {
		return err
	}
	return s.store.SetConfig(ctx, configStateTokenExpiresAt, "")
}

func (s *Service) persistOverrides(ctx context.Context, req AskRequest) error {
	if strings.TrimSpace(req.Endpoint) != "" {
		if err := s.store.SetConfig(ctx, ConfigEndpoint, strings.TrimSpace(req.Endpoint)); err != nil {
			return fmt.Errorf("set %s: %w", ConfigEndpoint, err)
		}
	} else if s.optionalConfig(ctx, ConfigEndpoint, "AIMA_SUPPORT_ENDPOINT") == "" {
		if err := s.store.SetConfig(ctx, ConfigEndpoint, DefaultEndpoint); err != nil {
			return fmt.Errorf("set default %s: %w", ConfigEndpoint, err)
		}
	}
	if strings.TrimSpace(req.InviteCode) != "" {
		if err := s.store.SetConfig(ctx, ConfigInviteCode, strings.TrimSpace(req.InviteCode)); err != nil {
			return fmt.Errorf("set %s: %w", ConfigInviteCode, err)
		}
	}
	if strings.TrimSpace(req.WorkerCode) != "" {
		if err := s.store.SetConfig(ctx, ConfigWorkerCode, strings.TrimSpace(req.WorkerCode)); err != nil {
			return fmt.Errorf("set %s: %w", ConfigWorkerCode, err)
		}
	}
	return nil
}

func (s *Service) persistActiveTask(ctx context.Context, taskID, status, target string) {
	current := s.loadTaskSnapshot(ctx, configStateActiveTaskID, configStateActiveTaskStatus, configStateActiveTaskTarget, configStateActiveTaskUpdatedAt)
	next := TaskSnapshot{
		TaskID: strings.TrimSpace(taskID),
		Status: strings.TrimSpace(status),
		Target: strings.TrimSpace(target),
	}
	if current.TaskID == next.TaskID && current.Status == next.Status && current.Target == next.Target {
		return
	}

	entries := map[string]string{
		configStateActiveTaskID:     next.TaskID,
		configStateActiveTaskStatus: next.Status,
		configStateActiveTaskTarget: next.Target,
	}
	if hasTaskSnapshot(next) || hasTaskSnapshot(current) {
		entries[configStateActiveTaskUpdatedAt] = s.now().UTC().Format(time.RFC3339)
	}
	s.persistStateEntries(ctx, entries)
}

func (s *Service) persistNotification(ctx context.Context, notification Notification) {
	now := s.now().UTC().Format(time.RFC3339)
	message := MessageSnapshot{
		Message: strings.TrimSpace(notification.Message),
		Type:    strings.TrimSpace(notification.Type),
		Level:   strings.TrimSpace(notification.Level),
		Phase:   strings.TrimSpace(notification.Phase),
	}
	if hasMessageSnapshot(message) {
		s.appendMessage(message)
		current := s.loadMessageSnapshot(ctx)
		if current.Message != message.Message || current.Type != message.Type || current.Level != message.Level || current.Phase != message.Phase {
			s.persistStateEntries(ctx, map[string]string{
				configStateLastMessage:          message.Message,
				configStateLastMessageType:      message.Type,
				configStateLastMessageLevel:     message.Level,
				configStateLastMessagePhase:     message.Phase,
				configStateLastMessageUpdatedAt: now,
			})
		}
	}

	s.persistSummaryNotification(ctx, notification)

	taskID := strings.TrimSpace(notification.TaskID)
	taskStatus := strings.TrimSpace(notification.TaskStatus)
	if taskID == "" && taskStatus == "" {
		return
	}

	current := s.loadTaskSnapshot(ctx, configStateLastTaskID, configStateLastTaskStatus, "", configStateLastTaskUpdatedAt)
	if current.TaskID != taskID || current.Status != taskStatus {
		s.persistStateEntries(ctx, map[string]string{
			configStateLastTaskID:        taskID,
			configStateLastTaskStatus:    taskStatus,
			configStateLastTaskUpdatedAt: now,
		})
	}
	s.persistActiveTask(ctx, "", "", "")
}

func (s *Service) persistSummaryNotification(ctx context.Context, notification Notification) {
	entries := map[string]string{}
	state := s.loadState(ctx)

	if referral := strings.TrimSpace(notification.ReferralCode); referral != "" && referral != state.ReferralCode {
		entries[configStateReferralCode] = referral
	}
	if share := strings.TrimSpace(notification.ShareText); share != "" && share != state.ShareText {
		entries[configStateShareText] = share
	}
	if notification.BudgetTasksTotal > 0 || notification.BudgetTasksRemaining > 0 {
		used := maxInt(notification.BudgetTasksTotal-notification.BudgetTasksRemaining, 0)
		if notification.BudgetTasksTotal != state.MaxTasks {
			entries[configStateMaxTasks] = strconv.Itoa(notification.BudgetTasksTotal)
		}
		if used != state.UsedTasks {
			entries[configStateUsedTasks] = strconv.Itoa(used)
		}
	}
	if notification.BudgetUSDTotal > 0 || notification.BudgetUSDRemaining > 0 {
		spent := notification.BudgetUSDTotal - notification.BudgetUSDRemaining
		if spent < 0 {
			spent = 0
		}
		if notification.BudgetUSDTotal != state.BudgetUSD {
			entries[configStateBudgetUSD] = strconv.FormatFloat(notification.BudgetUSDTotal, 'f', -1, 64)
		}
		if spent != state.SpentUSD {
			entries[configStateSpentUSD] = strconv.FormatFloat(spent, 'f', -1, 64)
		}
	}
	if len(entries) > 0 {
		s.persistStateEntries(ctx, entries)
	}
}

func (s *Service) persistStateEntries(ctx context.Context, entries map[string]string) {
	for key, value := range entries {
		if err := s.store.SetConfig(ctx, key, value); err != nil {
			s.logger.Warn("persist support state entry failed", "key", key, "error", err)
		}
	}
}

func (s *Service) loadTaskSnapshot(ctx context.Context, idKey, statusKey, targetKey, updatedKey string) TaskSnapshot {
	snapshot := TaskSnapshot{
		TaskID:    s.optionalConfig(ctx, idKey, ""),
		Status:    s.optionalConfig(ctx, statusKey, ""),
		UpdatedAt: s.optionalConfig(ctx, updatedKey, ""),
	}
	if targetKey != "" {
		snapshot.Target = s.optionalConfig(ctx, targetKey, "")
	}
	return snapshot
}

func (s *Service) loadMessageSnapshot(ctx context.Context) MessageSnapshot {
	return MessageSnapshot{
		Message:   s.optionalConfig(ctx, configStateLastMessage, ""),
		Type:      s.optionalConfig(ctx, configStateLastMessageType, ""),
		Level:     s.optionalConfig(ctx, configStateLastMessageLevel, ""),
		Phase:     s.optionalConfig(ctx, configStateLastMessagePhase, ""),
		UpdatedAt: s.optionalConfig(ctx, configStateLastMessageUpdatedAt, ""),
	}
}

func hasTaskSnapshot(snapshot TaskSnapshot) bool {
	return snapshot.TaskID != "" || snapshot.Status != "" || snapshot.Target != ""
}

func hasMessageSnapshot(snapshot MessageSnapshot) bool {
	return snapshot.Message != ""
}

func applyRegistrationSummary(state *deviceState, resp *selfRegisterResponse) {
	if state == nil || resp == nil {
		return
	}
	state.ShareText = strings.TrimSpace(resp.ShareText)
	if strings.TrimSpace(resp.ReferralCode) != "" {
		state.ReferralCode = strings.TrimSpace(resp.ReferralCode)
	}
	state.MaxTasks = resp.Budget.MaxTasks
	state.UsedTasks = resp.Budget.UsedTasks
	state.BudgetUSD = resp.Budget.BudgetUSD
	state.SpentUSD = resp.Budget.SpentUSD
	state.BudgetStatus = strings.TrimSpace(resp.Budget.Status)
	state.IsBound = resp.Budget.IsBound
	state.ReferralCount = resp.Budget.ReferralCount
}

func (s *Service) isEnabled(ctx context.Context) bool {
	value := s.optionalConfig(ctx, ConfigEnabled, "")
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

func (s *Service) endpointFromConfig(ctx context.Context) string {
	value := s.optionalConfig(ctx, ConfigEndpoint, "AIMA_SUPPORT_ENDPOINT")
	if value == "" {
		value = DefaultEndpoint
	}
	return normalizeEndpoint(value)
}

func (s *Service) optionalConfig(ctx context.Context, key, envKey string) string {
	if key != "" {
		if value, err := s.store.GetConfig(ctx, key); err == nil && strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	if envKey != "" {
		return strings.TrimSpace(os.Getenv(envKey))
	}
	return ""
}

func parseConfigInt(raw string) int {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0
	}
	value, err := strconv.Atoi(raw)
	if err != nil {
		return 0
	}
	return value
}

func parseConfigFloat(raw string) float64 {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0
	}
	value, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		return 0
	}
	return value
}

func parseConfigBool(raw string) bool {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}
