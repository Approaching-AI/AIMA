package ui

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRegisterRoutes_SupportManifest(t *testing.T) {
	t.Parallel()

	mux := http.NewServeMux()
	RegisterRoutes(&Deps{
		SupportManifest: func(ctx context.Context) (json.RawMessage, error) {
			_ = ctx
			return json.RawMessage(`{"flow_id":"device-go","blocks":{"task_menu":{"title":{"text":"Task menu"}}}}`), nil
		},
	})(mux)

	req := httptest.NewRequest(http.MethodGet, "/ui/api/support-manifest", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	if got := rec.Header().Get("Content-Type"); got != "application/json" {
		t.Fatalf("content-type = %q, want application/json", got)
	}
	if got := rec.Body.String(); got != `{"flow_id":"device-go","blocks":{"task_menu":{"title":{"text":"Task menu"}}}}` {
		t.Fatalf("body = %q", got)
	}
}

func TestRegisterRoutes_OnboardingManifest(t *testing.T) {
	t.Parallel()

	mux := http.NewServeMux()
	RegisterRoutes(&Deps{
		OnboardingManifest: func(ctx context.Context) (json.RawMessage, error) {
			_ = ctx
			return json.RawMessage(`{"version":"2026-03-31.1","locales":{"zh":{"title":"新手指南"}}}`), nil
		},
	})(mux)

	req := httptest.NewRequest(http.MethodGet, "/ui/api/onboarding-manifest", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	if got := rec.Header().Get("Content-Type"); got != "application/json" {
		t.Fatalf("content-type = %q, want application/json", got)
	}
	if got := rec.Header().Get("Cache-Control"); got != "no-cache, must-revalidate" {
		t.Fatalf("cache-control = %q, want no-cache, must-revalidate", got)
	}
	if got := rec.Body.String(); got != `{"version":"2026-03-31.1","locales":{"zh":{"title":"新手指南"}}}` {
		t.Fatalf("body = %q", got)
	}
}

func TestRegisterRoutes_OnboardingManifestProviderError(t *testing.T) {
	t.Parallel()

	mux := http.NewServeMux()
	RegisterRoutes(&Deps{
		OnboardingManifest: func(ctx context.Context) (json.RawMessage, error) {
			_ = ctx
			return nil, errors.New("manifest unavailable")
		},
	})(mux)

	req := httptest.NewRequest(http.MethodGet, "/ui/api/onboarding-manifest", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusBadGateway)
	}
	if got := rec.Header().Get("Content-Type"); got != "application/json" {
		t.Fatalf("content-type = %q, want application/json", got)
	}

	var body map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal body: %v", err)
	}
	if got := body["error"]; got != "manifest unavailable" {
		t.Fatalf("error = %q, want manifest unavailable", got)
	}
}

func TestRegisterRoutes_IndexIncludesOnboardingDrawerShell(t *testing.T) {
	t.Parallel()

	mux := http.NewServeMux()
	RegisterRoutes(nil)(mux)

	req := httptest.NewRequest(http.MethodGet, "/ui/", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}

	body := rec.Body.String()
	for _, token := range []string{
		`<template x-if="showOnboardingDrawer">`,
		`<aside class="onboarding-drawer" x-ref="onboardingDrawer"`,
		`class="agent-onboarding-btn" x-ref="onboardingTrigger" @click="openOnboardingDrawer()"`,
		`async loadOnboardingManifest(force)`,
		`const resp = await fetch('/ui/api/onboarding-manifest', { headers });`,
		`throw new Error('invalid onboarding manifest');`,
		`@keydown.tab.prevent="cycleOnboardingFocus($event)"`,
	} {
		if !strings.Contains(body, token) {
			t.Fatalf("body missing %q", token)
		}
	}
}

func TestRegisterRoutes_IndexWaitsForAIMAServePersistenceBeforeScan(t *testing.T) {
	t.Parallel()

	mux := http.NewServeMux()
	RegisterRoutes(nil)(mux)

	req := httptest.NewRequest(http.MethodGet, "/ui/", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	body := rec.Body.String()
	if !strings.Contains(body, "wizStackReadyForScan(stack)") {
		t.Fatal("index missing stack readiness helper")
	}
	if strings.Contains(body, `data.stack_status && (data.stack_status.docker === 'ready' || data.stack_status.k3s === 'ready')`) {
		t.Fatal("init polling still advances on docker/k3s readiness without checking needs_init")
	}
	if strings.Contains(body, `(this.onboardingData.stack_status.docker === 'ready' || this.onboardingData.stack_status.k3s === 'ready')`) {
		t.Fatal("init completion still advances on docker/k3s readiness without checking needs_init")
	}
}

func TestRegisterRoutes_IndexUsesConsolidatedPatrolTool(t *testing.T) {
	t.Parallel()

	mux := http.NewServeMux()
	RegisterRoutes(nil)(mux)

	req := httptest.NewRequest(http.MethodGet, "/ui/", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	body := rec.Body.String()
	if strings.Contains(body, "agent.patrol_config") {
		t.Fatal("index still references removed agent.patrol_config tool")
	}
	for _, token := range []string{
		`this.callTool('patrol', { action: 'config', config_action: 'get' })`,
		`this.callTool('patrol', { action: 'config', config_action: 'set', key: pk.key, value: pk.value })`,
		`this.callTool('patrol', { action: 'config', config_action: 'set', key: 'interval', value: '5m' })`,
	} {
		if !strings.Contains(body, token) {
			t.Fatalf("body missing %q", token)
		}
	}
}

func TestRegisterRoutes_IndexReadsBootstrapAPIKey(t *testing.T) {
	t.Parallel()

	mux := http.NewServeMux()
	RegisterRoutes(nil)(mux)

	req := httptest.NewRequest(http.MethodGet, "/ui/", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	body := rec.Body.String()
	for _, token := range []string{
		`window.__AIMA_BOOTSTRAP_API_KEY__ = "";`,
		`localStorage.getItem('aima_api_key') || window.__AIMA_BOOTSTRAP_API_KEY__ || ''`,
	} {
		if !strings.Contains(body, token) {
			t.Fatalf("body missing bootstrap API key token %q", token)
		}
	}
}

func TestRegisterRoutes_IndexScanResultsDoNotLookSelectable(t *testing.T) {
	t.Parallel()

	mux := http.NewServeMux()
	RegisterRoutes(nil)(mux)

	req := httptest.NewRequest(http.MethodGet, "/ui/", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	body := rec.Body.String()
	if strings.Contains(body, `item.type === 'model' ? '\u25A0'`) {
		t.Fatal("scan result model rows still use a checkbox-like square icon")
	}
	for _, token := range []string{
		`item.type === 'model' ? 'M'`,
		`x-show="onboardingScanDone && onboardingScanResults.models.length > 0"`,
		`@click="onboardingPhase = 'local'" x-text="t('wiz_choose_local')"`,
	} {
		if !strings.Contains(body, token) {
			t.Fatalf("body missing %q", token)
		}
	}
}

func TestRegisterRoutes_IndexLocalOnboardingDeploySkipsPull(t *testing.T) {
	t.Parallel()

	mux := http.NewServeMux()
	RegisterRoutes(nil)(mux)

	req := httptest.NewRequest(http.MethodGet, "/ui/", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	body := rec.Body.String()
	for _, token := range []string{
		`rec.no_pull || (rec.model_local && rec.engine_installed)`,
		`rec.engine_status && rec.engine_status.installed && rec.model_status && rec.model_status.local_available`,
		`no_pull: noPull`,
	} {
		if !strings.Contains(body, token) {
			t.Fatalf("onboarding deploy body missing %q", token)
		}
	}
	if !strings.Contains(body, `this.wizDeploy({ model: m.name || m.model, engine: m.engine || '', model_local: true, engine_installed: true, no_pull: true })`) {
		t.Fatal("local onboarding deploy does not request no_pull")
	}
}

func TestRegisterRoutes_IndexOffersExistingRunningModelChoice(t *testing.T) {
	t.Parallel()

	mux := http.NewServeMux()
	RegisterRoutes(nil)(mux)

	req := httptest.NewRequest(http.MethodGet, "/ui/", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	body := rec.Body.String()
	for _, token := range []string{
		`wizHasExistingService()`,
		`wizBestExistingService()`,
		`@click="wizUseExistingService(wizBestExistingService())"`,
		`fetch('/ui/api/onboarding-use-existing'`,
		`wiz_choose_existing`,
		`wiz_best_choice`,
	} {
		if !strings.Contains(body, token) {
			t.Fatalf("body missing existing-service onboarding token %q", token)
		}
	}
}

func TestRegisterRoutes_IndexShowsAPIAccessWithoutRenderingPrivateIP(t *testing.T) {
	t.Parallel()

	mux := http.NewServeMux()
	RegisterRoutes(nil)(mux)

	req := httptest.NewRequest(http.MethodGet, "/ui/", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	body := rec.Body.String()
	for _, token := range []string{
		`api_access`,
		`api_access_desc`,
		`apiBaseDisplay()`,
		`apiDeploymentChatCapable(deploymentDetailData)`,
		`api_non_chat_hint`,
		`copyCurrentAPIBaseURL($event)`,
		`copyAPICurl(deploymentDetailData, $event)`,
		`apiCurlTemplate(dep)`,
		`api_public_unconfigured`,
	} {
		if !strings.Contains(body, token) {
			t.Fatalf("body missing API access token %q", token)
		}
	}
	if strings.Contains(body, `x-text="apiCurrentBaseURL()"`) {
		t.Fatal("API access UI renders the current browser origin directly")
	}
}

func TestRegisterRoutes_IndexDoesNotShowChatExamplesForUnknownModelType(t *testing.T) {
	t.Parallel()

	mux := http.NewServeMux()
	RegisterRoutes(nil)(mux)

	req := httptest.NewRequest(http.MethodGet, "/ui/", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	body := rec.Body.String()
	start := strings.Index(body, "apiDeploymentChatCapable(dep) {")
	if start == -1 {
		t.Fatal("apiDeploymentChatCapable not found")
	}
	end := strings.Index(body[start:], "\n    }")
	if end == -1 {
		t.Fatal("could not isolate apiDeploymentChatCapable body")
	}
	fnBody := body[start : start+end]
	if strings.Contains(fnBody, "if (!kind) return true") {
		t.Fatalf("unknown model type should not default to chat-capable, body=%s", fnBody)
	}
	if !strings.Contains(fnBody, "if (!kind) return false") {
		t.Fatalf("unknown model type should return false, body=%s", fnBody)
	}
}

func TestRegisterRoutes_IndexMasksDevicePrivateAddress(t *testing.T) {
	t.Parallel()

	mux := http.NewServeMux()
	RegisterRoutes(nil)(mux)

	req := httptest.NewRequest(http.MethodGet, "/ui/", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	body := rec.Body.String()
	if strings.Contains(body, `class="device-ip" x-show="selfIp`) || strings.Contains(body, `x-text="selfIp"`) {
		t.Fatal("device identity still renders selfIp directly")
	}
	for _, token := range []string{
		`privateAddressLabel()`,
		`private_address_hidden`,
	} {
		if !strings.Contains(body, token) {
			t.Fatalf("body missing private-address masking token %q", token)
		}
	}
}

func TestRegisterRoutes_IndexIncludesOnboardingInteractionHelpers(t *testing.T) {
	t.Parallel()

	mux := http.NewServeMux()
	RegisterRoutes(nil)(mux)

	req := httptest.NewRequest(http.MethodGet, "/ui/", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	body := rec.Body.String()
	for _, token := range []string{
		"insertOnboardingCommand(command)",
		"defaultOnboardingManifest()",
		"resolvedOnboardingManifest()",
		"onboarding-command-btn",
		"in (group.items || [])",
		`x-text="onboardingText(item.label, item.command || '')"`,
		"onboardingLoadFailed: false",
		"_onboardingReturnFocus: null",
		"onboardingFocusableElements()",
		"focusOnboardingDrawer()",
		"cycleOnboardingFocus(e)",
		"restoreOnboardingFocus()",
		"if (this.onboardingLoadFailed) return this.defaultOnboardingManifest();",
		"this._onboardingReturnFocus = document.activeElement",
		"this.mobileTab = 'chat';",
		"restoreTarget && restoreTarget.isConnected",
		"if (this.showOnboardingDrawer)",
		"key === 'escape'",
		"key === 'k'",
	} {
		if !strings.Contains(body, token) {
			t.Fatalf("body missing %q", token)
		}
	}
}

func TestRegisterRoutes_IndexOnboardingInsertIsFillOnly(t *testing.T) {
	t.Parallel()

	mux := http.NewServeMux()
	RegisterRoutes(nil)(mux)

	req := httptest.NewRequest(http.MethodGet, "/ui/", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	body := rec.Body.String()
	start := strings.Index(body, "insertOnboardingCommand(command) {")
	if start == -1 {
		t.Fatal("insertOnboardingCommand not found")
	}
	end := strings.Index(body[start:], "\n\n    openOnboardingDrawer() {")
	if end == -1 {
		t.Fatal("could not isolate insertOnboardingCommand body")
	}
	fnBody := body[start : start+end]

	for _, token := range []string{
		"this.currentView = 'chat';",
		"this.mobileTab = 'chat';",
		"this.input = command;",
		"this.closeOnboardingDrawer();",
	} {
		if !strings.Contains(fnBody, token) {
			t.Fatalf("insertOnboardingCommand missing %q", token)
		}
	}
	if strings.Contains(fnBody, "this.send(") || strings.Contains(fnBody, "await this.send(") {
		t.Fatalf("insertOnboardingCommand should not auto-send, body=%s", fnBody)
	}
}

func TestRegisterRoutes_IndexFallbackOnboardingUsesCLICommands(t *testing.T) {
	t.Parallel()

	mux := http.NewServeMux()
	RegisterRoutes(nil)(mux)

	req := httptest.NewRequest(http.MethodGet, "/ui/", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	body := rec.Body.String()
	for _, token := range []string{
		`command: '/cli status'`,
		`command: '/cli hal detect'`,
		`command: '/cli model list'`,
		`command: '/cli engine list'`,
		`command: '/cli deploy list'`,
		`/cli status, /cli hal detect, and /cli model list`,
		`/cli status、/cli hal detect、/cli model list`,
	} {
		if !strings.Contains(body, token) {
			t.Fatalf("fallback onboarding missing %q", token)
		}
	}
}

func TestRegisterRoutes_IndexOnlyAllowsOpenAIExternalServiceImport(t *testing.T) {
	t.Parallel()

	mux := http.NewServeMux()
	RegisterRoutes(nil)(mux)

	req := httptest.NewRequest(http.MethodGet, "/ui/", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	body := rec.Body.String()
	start := strings.Index(body, "externalServiceImportable(service) {")
	if start == -1 {
		t.Fatal("externalServiceImportable not found")
	}
	end := strings.Index(body[start:], "\n    }")
	if end == -1 {
		t.Fatal("could not isolate externalServiceImportable body")
	}
	fnBody := body[start : start+end]
	if !strings.Contains(fnBody, "service.kind === 'openai'") {
		t.Fatalf("externalServiceImportable should allow openai services, body=%s", fnBody)
	}
	if strings.Contains(fnBody, "healthz") {
		t.Fatalf("externalServiceImportable should not allow healthz imports, body=%s", fnBody)
	}
}

func TestRegisterRoutes_IndexIncludesDeploymentStageFeedback(t *testing.T) {
	t.Parallel()

	mux := http.NewServeMux()
	RegisterRoutes(nil)(mux)

	req := httptest.NewRequest(http.MethodGet, "/ui/", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	body := rec.Body.String()
	for _, token := range []string{
		"startup_progress",
		"deployment-service-card",
		"deploymentShowProgress(dep)",
		"deploymentProgressValue(dep)",
		"deploymentProgressText(dep)",
		"openDeploymentDetail(dep)",
		"deploymentDetailOpen",
		"deploymentDetailRequestSeq",
		"this.callTool('deploy.status', { name })",
		"clearMissingGpuMemory: true",
		"deploymentGpuMemoryMiB(d)",
		"handleDeploymentStopClick($event, deploymentDetailData.name, { closeDetail: true })",
		"failure_detail: this.summarizeDeploymentFailure(d)",
		"summarizeDeploymentFailure(dep)",
	} {
		if !strings.Contains(body, token) {
			t.Fatalf("body missing %q", token)
		}
	}
}

func TestRegisterRoutes_IndexDeployDetailUsesBackendDefaultsAndImmediateClose(t *testing.T) {
	t.Parallel()

	mux := http.NewServeMux()
	RegisterRoutes(nil)(mux)

	req := httptest.NewRequest(http.MethodGet, "/ui/", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	body := rec.Body.String()
	for _, token := range []string{
		`this.callTool('deploy.defaults', { action: 'get', model: modelName })`,
		`this.callTool('deploy.defaults', { action: 'set', model: modelName, ...payload })`,
		`const data = await this.callTool('deploy.run', request);`,
		`this.deployDetailOpen = false;`,
		`await this.refreshDeployDryRun();`,
		`if (!kvApplied) suggestions.push({ key: 'kv_cache_dtype', value: 'fp8'`,
		`deploy_started_background`,
		`deploy_restore_recommended: 'Recommended parameters'`,
		`deploy_compat_title: 'Image architecture preflight'`,
		`deploy_compat_title: '\u955C\u50CF\u67B6\u6784\u9884\u68C0'`,
		`x-text="t('deploy_compat_title')"`,
		`handleDeployEngineChange($event)`,
		`handleDeployEngineChange(event) {`,
		`deployDryRunSeq: 0`,
		`const seq = ++this.deployDryRunSeq;`,
		`if (seq !== this.deployDryRunSeq) return;`,
		`this.deployDetailPlan = { ...this.deployDetailPlan, compatibility: null };`,
		`deployEngineRequestValue()`,
		`deploySelectedEngineMeta()`,
		`deployPlanMatchesSelectedEngine()`,
		`deployCompatibilityRows()`,
		`typeof c.image_available_in_docker === 'boolean'`,
		`typeof c.image_available_in_containerd === 'boolean'`,
		`.deploy-compat-grid,`,
		`model.detected_arch`,
		`this.callTool('scenario.apply', { name: this.deployScenarioName, bindings: scenarioBindings })`,
		`this.callTool('scenario.apply', {`,
		`this.callTool('scenario.show', { name: scenarioName })`,
		`x-text="t('deploy_cluster_config')"`,
	} {
		if !strings.Contains(body, token) {
			t.Fatalf("body missing deploy detail token %q", token)
		}
	}

	start := strings.Index(body, "async confirmDeployDetail() {")
	if start == -1 {
		t.Fatal("confirmDeployDetail not found")
	}
	end := strings.Index(body[start:], "\n    componentStatusNote(model)")
	if end == -1 {
		t.Fatal("could not isolate confirmDeployDetail body")
	}
	fnBody := body[start : start+end]
	closeIdx := strings.Index(fnBody, `this.deployDetailOpen = false;`)
	runIdx := strings.Index(fnBody, `const data = await this.callTool('deploy.run', request);`)
	if closeIdx == -1 || runIdx == -1 || closeIdx > runIdx {
		t.Fatalf("deploy detail should close before awaiting deploy.run, body=%s", fnBody)
	}

	start = strings.Index(body, "async refreshDeployDryRun(forceRecommended = false) {")
	if start == -1 {
		t.Fatal("refreshDeployDryRun not found")
	}
	end = strings.Index(body[start:], "\n    seedDeployDefaultParams()")
	if end == -1 {
		t.Fatal("could not isolate refreshDeployDryRun body")
	}
	fnBody = body[start : start+end]
	if strings.Contains(fnBody, "if (!modelName || this.deployDetailLoading) return;") {
		t.Fatalf("refreshDeployDryRun still drops newer preflights while loading, body=%s", fnBody)
	}
	if !strings.Contains(fnBody, "const requestEngine = forceRecommended ? '' : this.deployEngineRequestValue();") {
		t.Fatalf("refreshDeployDryRun should map selected engine before dry-run, body=%s", fnBody)
	}

	if strings.Contains(body, "aima_deploy_defaults:") || strings.Contains(body, "localStorage.setItem(this.deployDefaultsKey()") {
		t.Fatal("deploy defaults should not be stored only in browser localStorage")
	}
}

func TestRegisterRoutes_IndexIncludesDirectModeRoutingAndModelCards(t *testing.T) {
	t.Parallel()

	mux := http.NewServeMux()
	RegisterRoutes(nil)(mux)

	req := httptest.NewRequest(http.MethodGet, "/ui/", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}

	body := rec.Body.String()
	for _, token := range []string{
		"headerModeTone()",
		"agentModeTone()",
		"directQuickActions()",
		"inferDirectCommand(text)",
		"directMatchMessage(command)",
		"routeSelectedModel()",
		"routeSelectedEndpoint()",
		"routeSelectionLabel()",
		"configuredAgentModel()",
		"configuredAgentEndpoint()",
		"chat-mode-strip",
		"modelStatusNote(m.name)",
		"deployableModels()",
		"componentModels()",
		"componentStatusNote(m)",
		"model_components",
		"standalone_deploy",
		"rec.engine.name || rec.engine.type || ''",
		"model-entry-meta",
		"dep.name || dep.model || dep.address || dep.detail",
		"const nextDeployments = list.map(d => {",
		"nextDeployments.sort((a, b) => {",
		"agent_strategy",
		"selected_model",
		"configured_model",
		"direct_mode_ready",
	} {
		if !strings.Contains(body, token) {
			t.Fatalf("body missing %q", token)
		}
	}
}

func TestRegisterRoutes_IndexBootstrapsAPIKeyForLoopbackOnly(t *testing.T) {
	t.Parallel()

	mux := http.NewServeMux()
	RegisterRoutes(&Deps{
		APIKey: func(context.Context) string {
			return "local-secret"
		},
	})(mux)

	localReq := httptest.NewRequest(http.MethodGet, "/ui/", nil)
	localReq.Host = "127.0.0.1:6188"
	localReq.RemoteAddr = "127.0.0.1:51000"
	localRec := httptest.NewRecorder()
	mux.ServeHTTP(localRec, localReq)
	if localRec.Code != http.StatusOK {
		t.Fatalf("local status = %d, want %d", localRec.Code, http.StatusOK)
	}
	if !strings.Contains(localRec.Body.String(), `window.__AIMA_BOOTSTRAP_API_KEY__ = "local-secret";`) {
		t.Fatal("loopback UI did not receive bootstrap API key")
	}

	remoteReq := httptest.NewRequest(http.MethodGet, "/ui/", nil)
	remoteReq.Host = "192.168.110.184:6188"
	remoteReq.RemoteAddr = "127.0.0.1:51001"
	remoteRec := httptest.NewRecorder()
	mux.ServeHTTP(remoteRec, remoteReq)
	if remoteRec.Code != http.StatusOK {
		t.Fatalf("remote status = %d, want %d", remoteRec.Code, http.StatusOK)
	}
	if strings.Contains(remoteRec.Body.String(), "local-secret") {
		t.Fatal("non-loopback UI response leaked bootstrap API key")
	}
	if !strings.Contains(remoteRec.Body.String(), `window.__AIMA_BOOTSTRAP_API_KEY__ = "";`) {
		t.Fatal("non-loopback UI should keep an empty bootstrap API key")
	}
}

func TestRegisterRoutes_FaviconAssets(t *testing.T) {
	t.Parallel()

	mux := http.NewServeMux()
	RegisterRoutes(nil)(mux)

	t.Run("ui favicon svg", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/ui/favicon.svg", nil)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
		}
		if got := rec.Header().Get("Content-Type"); got != "image/svg+xml" {
			t.Fatalf("content-type = %q, want image/svg+xml", got)
		}
	})

	t.Run("root favicon redirect", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/favicon.ico", nil)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)

		if rec.Code != http.StatusFound {
			t.Fatalf("status = %d, want %d", rec.Code, http.StatusFound)
		}
		if got := rec.Header().Get("Location"); got != "/ui/favicon.ico" {
			t.Fatalf("location = %q, want /ui/favicon.ico", got)
		}
	})

	t.Run("apple touch icon png", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/ui/apple-touch-icon.png", nil)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
		}
		if got := rec.Header().Get("Content-Type"); got != "image/png" {
			t.Fatalf("content-type = %q, want image/png", got)
		}
	})
}
