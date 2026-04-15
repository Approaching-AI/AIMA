package agent

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// readOnlyDocs lists AIMA-generated fact documents the Explorer agent must not overwrite.
var readOnlyDocs = map[string]bool{
	"index.md":            true,
	"device-profile.md":   true,
	"available-combos.md": true,
	"knowledge-base.md":   true,
	"experiment-facts.md": true,
}

// ExplorerWorkspace manages the file workspace for an Explorer session.
// It enforces path safety (no directory escape) and read-only guards on
// AIMA-generated fact documents.
type ExplorerWorkspace struct {
	root string
}

// NewExplorerWorkspace creates a workspace rooted at root.
func NewExplorerWorkspace(root string) *ExplorerWorkspace {
	return &ExplorerWorkspace{root: root}
}

// Init creates the workspace directory structure.
func (w *ExplorerWorkspace) Init() error {
	if err := os.MkdirAll(filepath.Join(w.root, "experiments"), 0755); err != nil {
		return fmt.Errorf("init workspace: %w", err)
	}
	return nil
}

// EnsureWorkingDocuments creates writable session documents when missing and
// resets plan.md to the expected structure for the next phase.
func (w *ExplorerWorkspace) EnsureWorkingDocuments() error {
	if err := w.ensureFile("summary.md", defaultSummaryTemplate()); err != nil {
		return err
	}
	if err := w.WriteFile("plan.md", defaultPlanTemplate()); err != nil {
		return err
	}
	return nil
}

func (w *ExplorerWorkspace) ensureFile(rel, content string) error {
	p, err := w.safePath(rel)
	if err != nil {
		return err
	}
	if _, err := os.Stat(p); err == nil {
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("stat %s: %w", rel, err)
	}
	return w.WriteFile(rel, content)
}

// safePath resolves a relative path inside the workspace root and blocks escapes.
func (w *ExplorerWorkspace) safePath(rel string) (string, error) {
	abs := filepath.Join(w.root, filepath.FromSlash(rel))
	abs, err := filepath.Abs(abs)
	if err != nil {
		return "", fmt.Errorf("resolve path: %w", err)
	}
	rootAbs, err := filepath.Abs(w.root)
	if err != nil {
		return "", fmt.Errorf("resolve root: %w", err)
	}
	// Ensure abs is within root (must have root as prefix followed by separator or equal)
	if abs != rootAbs && !strings.HasPrefix(abs, rootAbs+string(filepath.Separator)) {
		return "", fmt.Errorf("path %q escapes workspace root", rel)
	}
	return abs, nil
}

// ReadFile reads a file relative to the workspace root.
func (w *ExplorerWorkspace) ReadFile(rel string) (string, error) {
	p, err := w.safePath(rel)
	if err != nil {
		return "", err
	}
	data, err := os.ReadFile(p)
	if err != nil {
		return "", fmt.Errorf("read %s: %w", rel, err)
	}
	return string(data), nil
}

// WriteFile writes content to a file relative to the workspace root.
// Blocks writes to read-only AIMA fact documents.
func (w *ExplorerWorkspace) WriteFile(rel, content string) error {
	if readOnlyDocs[filepath.Base(rel)] {
		return fmt.Errorf("%s is a read-only AIMA fact document", rel)
	}
	return w.writeFactDocument(rel, content)
}

// writeFactDocument writes content bypassing the read-only guard.
// Used internally for AIMA-generated fact documents.
func (w *ExplorerWorkspace) writeFactDocument(rel, content string) error {
	p, err := w.safePath(rel)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0755); err != nil {
		return fmt.Errorf("mkdir for %s: %w", rel, err)
	}
	if err := os.WriteFile(p, []byte(content), 0644); err != nil {
		return fmt.Errorf("write %s: %w", rel, err)
	}
	return nil
}

// AppendFile appends content to a file relative to the workspace root.
// Blocks appends to read-only AIMA fact documents.
func (w *ExplorerWorkspace) AppendFile(rel, content string) error {
	if readOnlyDocs[filepath.Base(rel)] {
		return fmt.Errorf("%s is a read-only AIMA fact document", rel)
	}
	p, err := w.safePath(rel)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0755); err != nil {
		return fmt.Errorf("mkdir for %s: %w", rel, err)
	}
	f, err := os.OpenFile(p, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return fmt.Errorf("open %s for append: %w", rel, err)
	}
	defer f.Close()
	if _, err := f.WriteString(content); err != nil {
		return fmt.Errorf("append %s: %w", rel, err)
	}
	return nil
}

// ListDir lists directory entries relative to the workspace root.
// Directories get a "/" suffix.
func (w *ExplorerWorkspace) ListDir(rel string) ([]string, error) {
	p, err := w.safePath(rel)
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(p)
	if err != nil {
		return nil, fmt.Errorf("list %s: %w", rel, err)
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() {
			name += "/"
		}
		names = append(names, name)
	}
	return names, nil
}

// GrepFile searches for pattern in a single file, returning "linenum:line" matches.
func (w *ExplorerWorkspace) GrepFile(pattern, rel string) ([]string, error) {
	re, err := regexp.Compile(pattern)
	if err != nil {
		return nil, fmt.Errorf("compile pattern %q: %w", pattern, err)
	}
	p, err := w.safePath(rel)
	if err != nil {
		return nil, err
	}
	f, err := os.Open(p)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", rel, err)
	}
	defer f.Close()
	var results []string
	scanner := bufio.NewScanner(f)
	lineNum := 0
	for scanner.Scan() {
		lineNum++
		line := scanner.Text()
		if re.MatchString(line) {
			results = append(results, fmt.Sprintf("%d:%s", lineNum, line))
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("scan %s: %w", rel, err)
	}
	return results, nil
}

// GrepDir searches for pattern across all files in a directory,
// returning "filename:linenum:line" matches.
func (w *ExplorerWorkspace) GrepDir(pattern, rel string) ([]string, error) {
	re, err := regexp.Compile(pattern)
	if err != nil {
		return nil, fmt.Errorf("compile pattern %q: %w", pattern, err)
	}
	p, err := w.safePath(rel)
	if err != nil {
		return nil, err
	}
	var results []string
	err = filepath.WalkDir(p, func(path string, d os.DirEntry, werr error) error {
		if werr != nil || d.IsDir() {
			return werr
		}
		f, err := os.Open(path)
		if err != nil {
			return nil // skip unreadable files
		}
		defer f.Close()
		rel, _ := filepath.Rel(p, path)
		scanner := bufio.NewScanner(f)
		lineNum := 0
		for scanner.Scan() {
			lineNum++
			line := scanner.Text()
			if re.MatchString(line) {
				results = append(results, fmt.Sprintf("%s:%d:%s", rel, lineNum, line))
			}
		}
		return scanner.Err()
	})
	if err != nil {
		return nil, fmt.Errorf("grep dir %s: %w", rel, err)
	}
	return results, nil
}

// yamlBlockRe matches a fenced yaml code block.
var yamlBlockRe = regexp.MustCompile("(?s)```ya?ml\n(.*?)```")

// parsePlanTasks extracts TaskSpec list from plan.md markdown.
// Looks for the yaml code block under "## Tasks".
// Returns nil, nil when the section exists but contains no tasks (valid for Act phase).
func parsePlanTasks(md string) ([]TaskSpec, error) {
	section := extractSection(md, "## Tasks")
	if section == "" {
		return nil, fmt.Errorf("no ## Tasks section found")
	}
	matches := yamlBlockRe.FindStringSubmatch(section)
	if len(matches) < 2 {
		// D2: No yaml block in ## Tasks is valid — the LLM may write prose
		// like "No new tasks needed" or omit the block entirely.
		return nil, nil
	}
	tasks, err := parseTaskSpecYAML([]byte(matches[1]))
	if err != nil {
		return nil, fmt.Errorf("parse tasks yaml: %w", err)
	}
	// D2: comment-only yaml blocks unmarshal to nil — also valid "no tasks".
	return tasks, nil
}

func parseTaskSpecYAML(data []byte) ([]TaskSpec, error) {
	var tasks []TaskSpec
	listErr := yaml.Unmarshal(data, &tasks)
	if listErr == nil {
		return tasks, nil
	}

	var wrapped struct {
		Tasks []TaskSpec `yaml:"tasks"`
	}
	if err := yaml.Unmarshal(data, &wrapped); err == nil {
		return wrapped.Tasks, nil
	}

	return nil, listErr
}

// parseRecommendedConfigs extracts RecommendedConfig list from summary.md.
// Looks for the yaml code block under "## Recommended Configurations".
func parseRecommendedConfigs(md string) ([]RecommendedConfig, error) {
	section := extractSection(md, "## Recommended Configurations")
	if section == "" {
		return nil, nil // no recommendations yet is normal
	}
	var configs []RecommendedConfig
	found, err := parseYAMLListSection(section, &configs)
	if err != nil {
		return nil, fmt.Errorf("parse recommendations yaml: %w", err)
	}
	if !found {
		return nil, nil
	}
	return configs, nil
}

// ConfirmedBlocker captures a machine-readable blocker discovered during the run.
type ConfirmedBlocker struct {
	Family     string `yaml:"family" json:"family"`
	Scope      string `yaml:"scope" json:"scope,omitempty"`
	Model      string `yaml:"model" json:"model,omitempty"`
	Engine     string `yaml:"engine" json:"engine,omitempty"`
	Reason     string `yaml:"reason" json:"reason"`
	RetryWhen  string `yaml:"retry_when" json:"retry_when,omitempty"`
	Confidence string `yaml:"confidence" json:"confidence,omitempty"`
}

// RetryDenyEntry captures a task or family that must not be retried this cycle.
type RetryDenyEntry struct {
	Model        string `yaml:"model" json:"model,omitempty"`
	Engine       string `yaml:"engine" json:"engine,omitempty"`
	ReasonFamily string `yaml:"reason_family" json:"reason_family"`
	Reason       string `yaml:"reason" json:"reason"`
}

// EvidenceLedgerEntry captures an evidence row used to ground later phases.
type EvidenceLedgerEntry struct {
	Source     string `yaml:"source" json:"source"`
	Kind       string `yaml:"kind" json:"kind"`
	Model      string `yaml:"model" json:"model,omitempty"`
	Engine     string `yaml:"engine" json:"engine,omitempty"`
	Evidence   string `yaml:"evidence" json:"evidence,omitempty"`
	Summary    string `yaml:"summary" json:"summary"`
	Confidence string `yaml:"confidence" json:"confidence,omitempty"`
}

func parseConfirmedBlockers(md string) ([]ConfirmedBlocker, error) {
	section := extractSection(md, "## Confirmed Blockers")
	if section == "" {
		return nil, nil
	}
	var blockers []ConfirmedBlocker
	found, err := parseYAMLListSection(section, &blockers)
	if err != nil {
		return nil, fmt.Errorf("parse confirmed blockers yaml: %w", err)
	}
	if !found {
		return nil, nil
	}
	return blockers, nil
}

func parseDoNotRetryThisCycle(md string) ([]RetryDenyEntry, error) {
	section := extractSection(md, "## Do Not Retry This Cycle")
	if section == "" {
		return nil, nil
	}
	var entries []RetryDenyEntry
	found, err := parseYAMLListSection(section, &entries)
	if err != nil {
		return nil, fmt.Errorf("parse do-not-retry yaml: %w", err)
	}
	if !found {
		return nil, nil
	}
	return entries, nil
}

func parseEvidenceLedger(md string) ([]EvidenceLedgerEntry, error) {
	section := extractSection(md, "## Evidence Ledger")
	if section == "" {
		return nil, nil
	}
	var entries []EvidenceLedgerEntry
	found, err := parseYAMLListSection(section, &entries)
	if err != nil {
		return nil, fmt.Errorf("parse evidence ledger yaml: %w", err)
	}
	if !found {
		return nil, nil
	}
	return entries, nil
}

func parseYAMLListSection(section string, out any) (bool, error) {
	matches := yamlBlockRe.FindStringSubmatch(section)
	if len(matches) < 2 {
		return false, nil
	}
	if err := yaml.Unmarshal([]byte(matches[1]), out); err != nil {
		return true, err
	}
	return true, nil
}

// RefreshFactDocuments regenerates all AIMA fact documents from current PlanInput.
// Uses writeFactDocument to bypass the read-only guard (these are AIMA-owned files).
func (w *ExplorerWorkspace) RefreshFactDocuments(input PlanInput) error {
	now := time.Now().Format("2006-01-02 15:04:05")
	docs := map[string]string{
		"index.md":            w.generateIndex(input, now),
		"device-profile.md":   w.generateDeviceProfile(input, now),
		"available-combos.md": w.generateAvailableCombos(input, now),
		"knowledge-base.md":   w.generateKnowledgeBase(input, now),
		"experiment-facts.md": w.generateExperimentFacts(now),
	}
	for name, content := range docs {
		if err := w.writeFactDocument(name, content); err != nil {
			return fmt.Errorf("refresh %s: %w", name, err)
		}
	}
	return nil
}

func (w *ExplorerWorkspace) generateIndex(input PlanInput, now string) string {
	readyCombos, blockedCombos, exploredCombos := comboFactCounts(input)

	var sb strings.Builder
	fmt.Fprintf(&sb, "# Explorer Index\n\n_Generated: %s_\n\n", now)
	fmt.Fprintf(&sb, "This workspace is the Explorer's file-based memory. Read this file first in every phase.\n\n")

	fmt.Fprintf(&sb, "## Mission\n\n")
	fmt.Fprintf(&sb, "- Build fact-grounded exploration plans for `%s`\n", input.Hardware.Profile)
	fmt.Fprintf(&sb, "- Prefer real executable discoveries over speculative tuning\n")
	fmt.Fprintf(&sb, "- Preserve high-signal notes about bugs, failure modes, and design doubts\n\n")

	fmt.Fprintf(&sb, "## Read Order\n\n")
	fmt.Fprintf(&sb, "1. `index.md`\n")
	fmt.Fprintf(&sb, "2. `available-combos.md`\n")
	fmt.Fprintf(&sb, "3. `device-profile.md`\n")
	fmt.Fprintf(&sb, "4. `knowledge-base.md`\n")
	fmt.Fprintf(&sb, "5. `experiment-facts.md`\n")
	fmt.Fprintf(&sb, "6. `summary.md`\n")
	fmt.Fprintf(&sb, "7. `experiments/`\n\n")

	fmt.Fprintf(&sb, "## Source Of Truth\n\n")
	fmt.Fprintf(&sb, "| Document | Owner | Writable | Purpose |\n")
	fmt.Fprintf(&sb, "|----------|-------|----------|---------|\n")
	fmt.Fprintf(&sb, "| index.md | AIMA | no | Workspace map, authority rules, required structure |\n")
	fmt.Fprintf(&sb, "| available-combos.md | AIMA | no | Authoritative ready/blocked combo frontier |\n")
	fmt.Fprintf(&sb, "| device-profile.md | AIMA | no | Hardware, local models, local engines, deployed state |\n")
	fmt.Fprintf(&sb, "| knowledge-base.md | AIMA | no | History, advisories, catalog capability hints |\n")
	fmt.Fprintf(&sb, "| experiment-facts.md | AIMA | no | Machine-generated digest of experiment outcomes and benchmark evidence |\n")
	fmt.Fprintf(&sb, "| plan.md | Agent | yes | Current executable plan for the next Do phase |\n")
	fmt.Fprintf(&sb, "| summary.md | Agent | yes | Running memory of findings, bugs, doubts, and strategy |\n")
	fmt.Fprintf(&sb, "| experiments/*.md | AIMA + Agent Notes | append notes only | Raw experiment outcomes |\n\n")

	fmt.Fprintf(&sb, "## Hard Rules\n\n")
	fmt.Fprintf(&sb, "- AIMA-generated fact documents are authoritative. If a fact is absent, treat it as unavailable.\n")
	fmt.Fprintf(&sb, "- New tasks may only use combos listed under `## Ready Combos` in `available-combos.md`.\n")
	fmt.Fprintf(&sb, "- Do not schedule any combo listed under `## Blocked Combos` in this round.\n")
	fmt.Fprintf(&sb, "- Do not infer standard engines, default images, or hidden model variants from prior knowledge.\n")
	fmt.Fprintf(&sb, "- The `query` tool supports only `search`, `compare`, `gaps`, and `aggregate`.\n")
	fmt.Fprintf(&sb, "- Keep the required headings in `plan.md` and `summary.md` so later phases can continue from them.\n\n")

	fmt.Fprintf(&sb, "## Current Fact Snapshot\n\n")
	fmt.Fprintf(&sb, "| Metric | Value |\n|--------|-------|\n")
	fmt.Fprintf(&sb, "| Hardware Profile | %s |\n", input.Hardware.Profile)
	fmt.Fprintf(&sb, "| Local Models | %d |\n", len(input.LocalModels))
	fmt.Fprintf(&sb, "| Local Engines | %d |\n", len(input.LocalEngines))
	fmt.Fprintf(&sb, "| Ready Combos | %d |\n", readyCombos)
	fmt.Fprintf(&sb, "| Blocked Combos | %d |\n", blockedCombos)
	fmt.Fprintf(&sb, "| Already Explored Combos | %d |\n\n", exploredCombos)

	fmt.Fprintf(&sb, "## Required Working Doc Structure\n\n")
	fmt.Fprintf(&sb, "`plan.md` should keep these sections:\n")
	fmt.Fprintf(&sb, "- `## Objective`\n")
	fmt.Fprintf(&sb, "- `## Fact Snapshot`\n")
	fmt.Fprintf(&sb, "- `## Task Board`\n")
	fmt.Fprintf(&sb, "- `## Tasks` with a YAML block\n\n")

	fmt.Fprintf(&sb, "`summary.md` should keep these sections:\n")
	fmt.Fprintf(&sb, "- `## Key Findings`\n")
	fmt.Fprintf(&sb, "- `## Bugs And Failures`\n")
	fmt.Fprintf(&sb, "- `## Design Doubts`\n")
	fmt.Fprintf(&sb, "- `## Recommended Configurations` with a YAML block\n")
	fmt.Fprintf(&sb, "- `## Confirmed Blockers` with a YAML block\n")
	fmt.Fprintf(&sb, "- `## Do Not Retry This Cycle` with a YAML block\n")
	fmt.Fprintf(&sb, "- `## Evidence Ledger` with a YAML block\n")
	fmt.Fprintf(&sb, "- `## Current Strategy`\n")
	fmt.Fprintf(&sb, "- `## Next Cycle Candidates`\n")

	return sb.String()
}

// generateDeviceProfile produces device-profile.md with hardware, models, engines, and active deployments.
func (w *ExplorerWorkspace) generateDeviceProfile(input PlanInput, now string) string {
	hw := input.Hardware
	totalVRAM := hw.VRAMMiB * hw.GPUCount
	if hw.GPUCount <= 1 {
		totalVRAM = hw.VRAMMiB
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "# Device Profile\n\n_Generated: %s_\n\n", now)

	// Hardware section
	fmt.Fprintf(&sb, "## Hardware\n\n")
	fmt.Fprintf(&sb, "| Field | Value |\n|-------|-------|\n")
	fmt.Fprintf(&sb, "| Profile | %s |\n", hw.Profile)
	fmt.Fprintf(&sb, "| GPU Arch | %s |\n", hw.GPUArch)
	fmt.Fprintf(&sb, "| GPU Count | %d |\n", hw.GPUCount)
	fmt.Fprintf(&sb, "| VRAM per GPU (MiB) | %d |\n", hw.VRAMMiB)
	fmt.Fprintf(&sb, "| Total VRAM (MiB) | %d |\n\n", totalVRAM)

	// Models table
	fmt.Fprintf(&sb, "## Local Models\n\n")
	fmt.Fprintf(&sb, "| Name | Format | Type | Size (GiB) | Max Context | Fits VRAM |\n")
	fmt.Fprintf(&sb, "|------|--------|------|------------|-------------|----------|\n")
	for _, m := range input.LocalModels {
		sizeGiB := float64(m.SizeBytes) / (1024 * 1024 * 1024)
		fits := "✅"
		reason := ""
		if !modelFitsVRAM(m.Name, input.LocalModels, totalVRAM) {
			fits = "❌"
			reason = " (VRAM overflow)"
		}
		ctxStr := "—"
		if m.MaxContextLen > 0 {
			ctxStr = fmt.Sprintf("%dK", m.MaxContextLen/1024)
		}
		fmt.Fprintf(&sb, "| %s | %s | %s | %.2f | %s | %s%s |\n", m.Name, m.Format, m.Type, sizeGiB, ctxStr, fits, reason)
	}
	fmt.Fprintf(&sb, "\n")

	// Engines table
	fmt.Fprintf(&sb, "## Local Engines\n\n")
	fmt.Fprintf(&sb, "| Type | Runtime | Deploy Artifact | Features | Tunable Params |\n")
	fmt.Fprintf(&sb, "|------|---------|-----------------|----------|----------------|\n")
	for _, e := range input.LocalEngines {
		features := strings.Join(e.Features, ", ")
		paramKeys := make([]string, 0, len(e.TunableParams))
		for k := range e.TunableParams {
			paramKeys = append(paramKeys, k)
		}
		params := strings.Join(paramKeys, ", ")
		artifact := e.Artifact
		if artifact == "" {
			artifact = "_n/a_"
		}
		fmt.Fprintf(&sb, "| %s | %s | %s | %s | %s |\n", e.Type, e.Runtime, artifact, features, params)
	}
	fmt.Fprintf(&sb, "\n")

	// Active deployments
	fmt.Fprintf(&sb, "## Active Deployments\n\n")
	if len(input.ActiveDeploys) == 0 {
		fmt.Fprintf(&sb, "_None_\n")
	} else {
		fmt.Fprintf(&sb, "| Model | Engine | Status |\n|-------|--------|--------|\n")
		for _, d := range input.ActiveDeploys {
			fmt.Fprintf(&sb, "| %s | %s | %s |\n", d.Model, d.Engine, d.Status)
		}
	}
	fmt.Fprintf(&sb, "\n")

	return sb.String()
}

// generateAvailableCombos produces available-combos.md classifying all model×engine pairs.
func (w *ExplorerWorkspace) generateAvailableCombos(input PlanInput, now string) string {
	if len(input.ComboFacts) > 0 {
		return w.generateResolvedAvailableCombos(input, now)
	}

	hw := input.Hardware
	totalVRAM := hw.VRAMMiB * hw.GPUCount
	if hw.GPUCount <= 1 {
		totalVRAM = hw.VRAMMiB
	}

	// Build skip set for quick lookup
	skipSet := make(map[string]string) // "model|engine" → reason
	for _, s := range input.SkipCombos {
		skipSet[s.Model+"|"+s.Engine] = s.Reason
	}

	type comboRow struct {
		model, engine, reason string
	}
	var unexplored, explored, incompatible []comboRow

	for _, m := range input.LocalModels {
		for _, e := range input.LocalEngines {
			key := m.Name + "|" + e.Type

			// Check incompatibility
			var incompat string
			if !engineFormatCompatible(e.Type, m.Format) {
				incompat = fmt.Sprintf("format mismatch (%s vs %s)", e.Type, m.Format)
			} else if !engineSupportsModelTypeFromList(e.SupportedModelTypes, m.Type) {
				incompat = fmt.Sprintf("type mismatch (%s does not support %s)", e.Type, m.Type)
			} else if !modelFitsVRAM(m.Name, input.LocalModels, totalVRAM) {
				incompat = "VRAM overflow"
			}

			if incompat != "" {
				incompatible = append(incompatible, comboRow{m.Name, e.Type, incompat})
				continue
			}

			if reason, ok := skipSet[key]; ok {
				explored = append(explored, comboRow{m.Name, e.Type, reason})
				continue
			}

			unexplored = append(unexplored, comboRow{m.Name, e.Type, ""})
		}
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "# Available Combos\n\n_Generated: %s_\n\n", now)
	fmt.Fprintf(&sb, "_Resolver-backed combo facts unavailable; this is a coarse compatibility fallback._\n\n")

	fmt.Fprintf(&sb, "## Ready Combos\n\n")
	if len(unexplored) == 0 {
		fmt.Fprintf(&sb, "_None_\n\n")
	} else {
		fmt.Fprintf(&sb, "| Model | Engine | Reason |\n|-------|--------|--------|\n")
		for _, r := range unexplored {
			fmt.Fprintf(&sb, "| %s | %s | coarse local compatibility only |\n", r.model, r.engine)
		}
		fmt.Fprintf(&sb, "\n")
	}

	fmt.Fprintf(&sb, "## Already Explored\n\n")
	if len(explored) == 0 {
		fmt.Fprintf(&sb, "_None_\n\n")
	} else {
		fmt.Fprintf(&sb, "| Model | Engine | Reason |\n|-------|--------|--------|\n")
		for _, r := range explored {
			fmt.Fprintf(&sb, "| %s | %s | %s |\n", r.model, r.engine, r.reason)
		}
		fmt.Fprintf(&sb, "\n")
	}

	fmt.Fprintf(&sb, "## Blocked Combos\n\n")
	if len(incompatible) == 0 {
		fmt.Fprintf(&sb, "_None_\n\n")
	} else {
		fmt.Fprintf(&sb, "| Model | Engine | Reason |\n|-------|--------|--------|\n")
		for _, r := range incompatible {
			fmt.Fprintf(&sb, "| %s | %s | %s |\n", r.model, r.engine, r.reason)
		}
		fmt.Fprintf(&sb, "\n")
	}

	return sb.String()
}

func (w *ExplorerWorkspace) generateResolvedAvailableCombos(input PlanInput, now string) string {
	skipSet := make(map[string]string, len(input.SkipCombos))
	for _, s := range input.SkipCombos {
		skipSet[s.Model+"|"+s.Engine] = s.Reason
	}

	var ready, explored, blocked []ComboFact
	for _, fact := range input.ComboFacts {
		key := fact.Model + "|" + fact.Engine
		if reason, ok := skipSet[key]; ok {
			fact.Reason = reason
			explored = append(explored, fact)
			continue
		}
		if fact.Status == "ready" {
			ready = append(ready, fact)
			continue
		}
		blocked = append(blocked, fact)
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "# Available Combos\n\n_Generated: %s_\n\n", now)
	fmt.Fprintf(&sb, "This document is authoritative for new scheduling. Only rows under `## Ready Combos` may appear in new tasks.\n")
	fmt.Fprintf(&sb, "This document is refreshed before each PDCA phase; plan.md snapshots may refer to an earlier state.\n\n")

	fmt.Fprintf(&sb, "## Ready Combos\n\n")
	writeComboTable(&sb, ready, "ready")

	fmt.Fprintf(&sb, "## Already Explored\n\n")
	writeComboTable(&sb, explored, "explored")

	fmt.Fprintf(&sb, "## Blocked Combos\n\n")
	writeComboTable(&sb, blocked, "blocked")

	return sb.String()
}

// generateKnowledgeBase produces knowledge-base.md with advisories, history, and engine catalog capabilities.
func (w *ExplorerWorkspace) generateKnowledgeBase(input PlanInput, now string) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "# Knowledge Base\n\n_Generated: %s_\n\n", now)

	// Advisories
	fmt.Fprintf(&sb, "## Advisories\n\n")
	if len(input.Advisories) == 0 {
		fmt.Fprintf(&sb, "_No advisories_\n\n")
	} else {
		fmt.Fprintf(&sb, "| ID | Type | Model | Engine | Confidence | Reasoning |\n")
		fmt.Fprintf(&sb, "|----|------|-------|--------|------------|----------|\n")
		for _, a := range input.Advisories {
			fmt.Fprintf(&sb, "| %s | %s | %s | %s | %s | %s |\n",
				a.ID, a.Type, a.TargetModel, a.TargetEngine, a.Confidence, a.Reasoning)
		}
		fmt.Fprintf(&sb, "\n")
	}

	// Recent History
	fmt.Fprintf(&sb, "## Recent History\n\n")
	if len(input.History) == 0 {
		fmt.Fprintf(&sb, "_No history_\n\n")
	} else {
		fmt.Fprintf(&sb, "| Model | Engine | Kind | Status | Goal |\n")
		fmt.Fprintf(&sb, "|-------|--------|------|--------|------|\n")
		for _, h := range input.History {
			fmt.Fprintf(&sb, "| %s | %s | %s | %s | %s |\n",
				h.ModelID, h.EngineID, h.Kind, h.Status, h.Goal)
		}
		fmt.Fprintf(&sb, "\n")
	}

	// Frontier coverage guidance
	fmt.Fprintf(&sb, "## Frontier Coverage\n\n")
	coverageRows := readyCoverageRows(input)
	if len(coverageRows) == 0 {
		fmt.Fprintf(&sb, "_No ready frontier coverage data_\n\n")
	} else {
		fmt.Fprintf(&sb, "| Model | Ready Engines | In Recent History | Guidance |\n")
		fmt.Fprintf(&sb, "|-------|---------------|-------------------|----------|\n")
		for _, row := range coverageRows {
			recent := "no"
			guidance := "prefer for new coverage"
			if row.Recent {
				recent = "yes"
				guidance = "only pivot again if this narrows a specific blocker"
			}
			fmt.Fprintf(&sb, "| %s | %s | %s | %s |\n",
				row.Model, strings.Join(row.Engines, ", "), recent, guidance)
		}
		fmt.Fprintf(&sb, "\n")
	}

	// Catalog Engine Capabilities
	fmt.Fprintf(&sb, "## Catalog Engine Capabilities\n\n")
	if len(input.LocalEngines) == 0 {
		fmt.Fprintf(&sb, "_No engines_\n\n")
	} else {
		fmt.Fprintf(&sb, "| Engine | Runtime | Features | Notes |\n")
		fmt.Fprintf(&sb, "|--------|---------|----------|-------|\n")
		for _, e := range input.LocalEngines {
			features := strings.Join(e.Features, ", ")
			fmt.Fprintf(&sb, "| %s | %s | %s | %s |\n", e.Type, e.Runtime, features, e.Notes)
		}
		fmt.Fprintf(&sb, "\n")
	}

	return sb.String()
}

type ExperimentRecord struct {
	Path   string
	Task   TaskSpec
	Result ExperimentResult
}

func (w *ExplorerWorkspace) LoadExperimentRecords() ([]ExperimentRecord, error) {
	pattern, err := w.safePath("experiments/*.md")
	if err != nil {
		return nil, err
	}
	paths, err := filepath.Glob(pattern)
	if err != nil {
		return nil, fmt.Errorf("glob experiments: %w", err)
	}
	records := make([]ExperimentRecord, 0, len(paths))
	for _, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("read experiment %s: %w", path, err)
		}
		task, result := recoverExperimentRecordMarkdown(string(data))
		rel, _ := filepath.Rel(w.root, path)
		records = append(records, ExperimentRecord{
			Path:   filepath.ToSlash(rel),
			Task:   task,
			Result: result,
		})
	}
	return records, nil
}

func recoverExperimentRecordMarkdown(md string) (TaskSpec, ExperimentResult) {
	task, taskErr := parseExperimentTaskMarkdown(md)
	if taskErr != nil {
		return TaskSpec{}, ExperimentResult{
			Status: "invalid_record",
			Error:  fmt.Sprintf("parse task yaml: %v", taskErr),
		}
	}
	result, resultErr := parseExperimentResultMarkdown(md)
	if resultErr != nil {
		result = ExperimentResult{
			Status: "invalid_record",
			Error:  fmt.Sprintf("parse result yaml: %v", resultErr),
		}
	}
	return task, result
}

func parseExperimentRecordMarkdown(md string) (TaskSpec, ExperimentResult, error) {
	task, err := parseExperimentTaskMarkdown(md)
	if err != nil {
		return TaskSpec{}, ExperimentResult{}, err
	}
	result, err := parseExperimentResultMarkdown(md)
	if err != nil {
		return task, ExperimentResult{}, err
	}
	return task, result, nil
}

func parseExperimentTaskMarkdown(md string) (TaskSpec, error) {
	var task TaskSpec
	taskSection := extractSection(md, "## Task")
	taskBlock := yamlBlockRe.FindStringSubmatch(taskSection)
	if len(taskBlock) < 2 {
		return task, nil
	}
	if err := yaml.Unmarshal([]byte(taskBlock[1]), &task); err != nil {
		return TaskSpec{}, fmt.Errorf("parse task yaml: %w", err)
	}
	return task, nil
}

func parseExperimentResultMarkdown(md string) (ExperimentResult, error) {
	var result ExperimentResult
	resultSection := extractSection(md, "## Result")
	resultBlock := yamlBlockRe.FindStringSubmatch(resultSection)
	if len(resultBlock) < 2 {
		return result, fmt.Errorf("missing result yaml block")
	}
	if err := yaml.Unmarshal([]byte(resultBlock[1]), &result); err != nil {
		return ExperimentResult{}, fmt.Errorf("parse result yaml: %w", err)
	}
	return result, nil
}

func (w *ExplorerWorkspace) generateExperimentFacts(now string) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "# Experiment Facts\n\n_Generated: %s_\n\n", now)
	records, err := w.LoadExperimentRecords()
	if err != nil {
		fmt.Fprintf(&sb, "_Unavailable: %v_\n", err)
		return sb.String()
	}
	if len(records) == 0 {
		fmt.Fprintf(&sb, "_No experiments yet._\n")
		return sb.String()
	}
	fmt.Fprintf(&sb, "| Experiment | Model | Engine | Status | Signal | Benchmarks | Best Rate | Success Cells | Error |\n")
	fmt.Fprintf(&sb, "|------------|-------|--------|--------|--------|------------|-----------|---------------|-------|\n")
	for _, rec := range records {
		bestRate := 0.0
		for _, bench := range rec.Result.Benchmarks {
			if bench.ThroughputTPS > bestRate {
				bestRate = bench.ThroughputTPS
			}
		}
		errText := strings.TrimSpace(rec.Result.Error)
		if errText == "" {
			errText = "—"
		}
		fmt.Fprintf(&sb, "| %s | %s | %s | %s | %s | %d | %.1f | %d/%d | %s |\n",
			filepath.Base(rec.Path), rec.Task.Model, rec.Task.Engine, rec.Result.Status,
			experimentSignal(rec), len(rec.Result.Benchmarks), bestRate, rec.Result.SuccessCells, rec.Result.MatrixCells, errText)
	}
	fmt.Fprintf(&sb, "\n## Hard Facts\n\n")
	fmt.Fprintf(&sb, "- Treat this file as the machine-generated digest of experiments/*.md.\n")
	fmt.Fprintf(&sb, "- If summary.md conflicts with this file, this file wins.\n")
	fmt.Fprintf(&sb, "- `Signal=benchmark_ok` means the combo completed with usable benchmark evidence.\n")
	fmt.Fprintf(&sb, "- `Signal=inference_no_output` means deploy reached benchmark execution but produced no usable outputs.\n")
	fmt.Fprintf(&sb, "- A recommendation without a matching successful experiment in this file must stay provisional.\n")
	return sb.String()
}

func experimentSignal(rec ExperimentRecord) string {
	status := strings.ToLower(strings.TrimSpace(rec.Result.Status))
	switch status {
	case "invalid_record":
		return "invalid_record"
	case "completed":
		if rec.Result.SuccessCells > 0 || rec.Result.BenchmarkID != "" || rec.Result.ConfigID != "" {
			return "benchmark_ok"
		}
	}
	if len(rec.Result.Benchmarks) > 0 && rec.Result.SuccessCells == 0 {
		return "inference_no_output"
	}
	errText := strings.ToLower(strings.TrimSpace(rec.Result.Error))
	switch {
	case strings.Contains(errText, "pre-flight deploy"),
		strings.Contains(errText, "wait for deployed"),
		strings.Contains(errText, "deploy apply"),
		strings.Contains(errText, "stalled at"):
		return "deploy_failed"
	case status != "":
		return status
	default:
		return "unknown"
	}
}

type coverageRow struct {
	Model   string
	Engines []string
	Recent  bool
}

func readyCoverageRows(input PlanInput) []coverageRow {
	if len(input.ComboFacts) == 0 {
		return nil
	}
	skipSet := make(map[string]struct{}, len(input.SkipCombos))
	for _, s := range input.SkipCombos {
		skipSet[s.Model+"|"+s.Engine] = struct{}{}
	}
	recentModels := make(map[string]bool, len(input.History))
	for _, h := range input.History {
		model := strings.TrimSpace(h.ModelID)
		if model != "" {
			recentModels[model] = true
		}
	}
	engineSetByModel := make(map[string]map[string]struct{})
	for _, fact := range input.ComboFacts {
		if fact.Status != "ready" {
			continue
		}
		key := fact.Model + "|" + fact.Engine
		if _, skipped := skipSet[key]; skipped {
			continue
		}
		if _, ok := engineSetByModel[fact.Model]; !ok {
			engineSetByModel[fact.Model] = make(map[string]struct{})
		}
		engineSetByModel[fact.Model][fact.Engine] = struct{}{}
	}
	rows := make([]coverageRow, 0, len(engineSetByModel))
	for model, engines := range engineSetByModel {
		engineList := make([]string, 0, len(engines))
		for engine := range engines {
			engineList = append(engineList, engine)
		}
		sort.Strings(engineList)
		rows = append(rows, coverageRow{
			Model:   model,
			Engines: engineList,
			Recent:  recentModels[model],
		})
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].Recent != rows[j].Recent {
			return !rows[i].Recent
		}
		if len(rows[i].Engines) != len(rows[j].Engines) {
			return len(rows[i].Engines) > len(rows[j].Engines)
		}
		return strings.ToLower(rows[i].Model) < strings.ToLower(rows[j].Model)
	})
	return rows
}

func comboFactCounts(input PlanInput) (ready, blocked, explored int) {
	skipSet := make(map[string]struct{}, len(input.SkipCombos))
	for _, s := range input.SkipCombos {
		skipSet[s.Model+"|"+s.Engine] = struct{}{}
	}
	if len(input.ComboFacts) == 0 {
		return 0, 0, len(input.SkipCombos)
	}
	for _, fact := range input.ComboFacts {
		if _, ok := skipSet[fact.Model+"|"+fact.Engine]; ok {
			explored++
			continue
		}
		if fact.Status == "ready" {
			ready++
			continue
		}
		blocked++
	}
	return ready, blocked, explored
}

func writeComboTable(sb *strings.Builder, facts []ComboFact, mode string) {
	if len(facts) == 0 {
		fmt.Fprintf(sb, "_None_\n\n")
		return
	}
	fmt.Fprintf(sb, "| Model | Engine | Runtime | Deploy Artifact | Reason |\n")
	fmt.Fprintf(sb, "|-------|--------|---------|-----------------|--------|\n")
	for _, fact := range facts {
		runtime := fact.Runtime
		if runtime == "" {
			runtime = "_n/a_"
		}
		artifact := fact.Artifact
		if artifact == "" {
			artifact = "_n/a_"
		}
		reason := strings.TrimSpace(fact.Reason)
		if reason == "" {
			switch mode {
			case "ready":
				reason = "resolver and local no-pull runtime checks passed"
			case "explored":
				reason = "already explored"
			default:
				reason = "blocked"
			}
		}
		fmt.Fprintf(sb, "| %s | %s | %s | %s | %s |\n", fact.Model, fact.Engine, runtime, artifact, reason)
	}
	fmt.Fprintf(sb, "\n")
}

func defaultPlanTemplate() string {
	return "# Exploration Plan\n\n" +
		"## Objective\n\n" +
		"Summarize the next most valuable fact-grounded experiments for this device.\n\n" +
		"## Fact Snapshot\n\n" +
		"- Fill after reading index.md, available-combos.md, and knowledge-base.md frontier coverage.\n\n" +
		"## Task Board\n\n" +
		"- [ ] Read index.md\n" +
		"- [ ] Read available-combos.md\n" +
		"- [ ] Read knowledge-base.md frontier coverage\n" +
		"- [ ] Read summary.md blockers and evidence\n" +
		"- [ ] Give at least one slot to a ready model not seen in recent history when such models exist\n" +
		"- [ ] Write only executable tasks from Ready Combos not on Do Not Retry This Cycle\n\n" +
		"## Tasks\n" +
		"```yaml\n[]\n```\n"
}

func defaultSummaryTemplate() string {
	return "# Exploration Summary\n\n" +
		"## Key Findings\n\n" +
		"_No findings yet._\n\n" +
		"## Bugs And Failures\n\n" +
		"_No bugs recorded yet._\n\n" +
		"## Confirmed Blockers\n\n" +
		"```yaml\n[]\n```\n\n" +
		"## Do Not Retry This Cycle\n\n" +
		"```yaml\n[]\n```\n\n" +
		"## Evidence Ledger\n\n" +
		"```yaml\n[]\n```\n\n" +
		"## Design Doubts\n\n" +
		"_No design doubts recorded yet._\n\n" +
		"## Recommended Configurations\n" +
		"```yaml\n[]\n```\n\n" +
		"## Current Strategy\n\n" +
		"Start from Ready Combos only. Treat Confirmed Blockers and Do Not Retry This Cycle as hard constraints.\n\n" +
		"## Next Cycle Candidates\n\n" +
		"_No candidates yet._\n"
}

func normalizeSummaryMarkdown(content string) string {
	trimmed := strings.TrimSpace(content)
	if trimmed == "" {
		return defaultSummaryTemplate()
	}
	required := []struct {
		heading string
		body    string
	}{
		{"## Key Findings", "_No findings yet._"},
		{"## Bugs And Failures", "_No bugs recorded yet._"},
		{"## Confirmed Blockers", "```yaml\n[]\n```"},
		{"## Do Not Retry This Cycle", "```yaml\n[]\n```"},
		{"## Evidence Ledger", "```yaml\n[]\n```"},
		{"## Design Doubts", "_No design doubts recorded yet._"},
		{"## Recommended Configurations", "```yaml\n[]\n```"},
		{"## Current Strategy", "Start from Ready Combos only. Treat Confirmed Blockers and Do Not Retry This Cycle as hard constraints."},
		{"## Next Cycle Candidates", "_No candidates yet._"},
	}
	var sb strings.Builder
	sb.WriteString(strings.TrimRight(trimmed, "\n"))
	for _, section := range required {
		if strings.Contains(trimmed, section.heading) {
			continue
		}
		fmt.Fprintf(&sb, "\n\n%s\n\n%s", section.heading, section.body)
	}
	sb.WriteString("\n")
	return sb.String()
}

// ExperimentResult records the outcome of a single experiment task.
type ExperimentResult struct {
	Status        string           `yaml:"status"`
	StartedAt     string           `yaml:"started_at"`
	DurationS     float64          `yaml:"duration_s"`
	ColdStartS    float64          `yaml:"cold_start_s,omitempty"`
	Error         string           `yaml:"error,omitempty"`
	BenchmarkID   string           `yaml:"benchmark_id,omitempty"`
	ConfigID      string           `yaml:"config_id,omitempty"`
	EngineVersion string           `yaml:"engine_version,omitempty"`
	EngineImage   string           `yaml:"engine_image,omitempty"`
	ResourceUsage map[string]any   `yaml:"resource_usage,omitempty"`
	DeployConfig  map[string]any   `yaml:"deploy_config,omitempty"`
	MatrixCells   int              `yaml:"matrix_cells,omitempty"`
	SuccessCells  int              `yaml:"success_cells,omitempty"`
	Benchmarks    []BenchmarkEntry `yaml:"benchmarks,omitempty"`
}

// BenchmarkEntry records a single benchmark data point.
type BenchmarkEntry struct {
	Profile       string         `yaml:"profile,omitempty"`
	Concurrency   int            `yaml:"concurrency"`
	InputTokens   int            `yaml:"input_tokens"`
	MaxTokens     int            `yaml:"max_tokens"`
	ThroughputTPS float64        `yaml:"throughput_tps,omitempty"`
	TTFTP95Ms     float64        `yaml:"ttft_p95_ms,omitempty"`
	TPOTP95Ms     float64        `yaml:"tpot_p95_ms,omitempty"`
	BenchmarkID   string         `yaml:"benchmark_id,omitempty"`
	ConfigID      string         `yaml:"config_id,omitempty"`
	EngineVersion string         `yaml:"engine_version,omitempty"`
	EngineImage   string         `yaml:"engine_image,omitempty"`
	ResourceUsage map[string]any `yaml:"resource_usage,omitempty"`
	Error         string         `yaml:"error,omitempty"`
}

// WriteExperimentResult writes experiments/NNN-model-engine.md for the given task and result.
// Uses writeFactDocument to bypass the read-only guard (experiments/ is AIMA-owned).
// Returns the relative path written.
func (w *ExplorerWorkspace) WriteExperimentResult(index int, task TaskSpec, result ExperimentResult) (string, error) {
	ordinal, err := w.allocateExperimentOrdinal(index)
	if err != nil {
		return "", err
	}
	taskYAML, err := yaml.Marshal(task)
	if err != nil {
		return "", fmt.Errorf("marshal task: %w", err)
	}
	resultYAML, err := yaml.Marshal(result)
	if err != nil {
		return "", fmt.Errorf("marshal result: %w", err)
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "# Experiment %03d: %s / %s\n\n", ordinal, task.Model, task.Engine)

	fmt.Fprintf(&sb, "## Task\n\n```yaml\n%s```\n\n", string(taskYAML))
	fmt.Fprintf(&sb, "## Result\n\n```yaml\n%s```\n\n", string(resultYAML))

	// Benchmark matrix table
	fmt.Fprintf(&sb, "## Benchmark Matrix\n\n")
	if len(result.Benchmarks) == 0 {
		fmt.Fprintf(&sb, "_No benchmark data_\n\n")
	} else {
		fmt.Fprintf(&sb, "| Profile | Concurrency | Input Tokens | Max Tokens | Throughput | TTFT P95 (ms) | TPOT P95 (ms) | Status |\n")
		fmt.Fprintf(&sb, "|---------|-------------|--------------|------------|------------------|---------------|---------------|--------|\n")
		for _, b := range result.Benchmarks {
			status := "ok"
			if strings.TrimSpace(b.Error) != "" {
				status = b.Error
			} else if b.ThroughputTPS == 0 && b.TTFTP95Ms == 0 {
				status = "no-output"
			}
			profile := "-"
			if strings.TrimSpace(b.Profile) != "" {
				profile = b.Profile
			}
			fmt.Fprintf(&sb, "| %s | %d | %d | %d | %.1f | %.0f | %.0f | %s |\n",
				profile, b.Concurrency, b.InputTokens, b.MaxTokens,
				b.ThroughputTPS, b.TTFTP95Ms, b.TPOTP95Ms, status)
		}
		fmt.Fprintf(&sb, "\n")
	}

	fmt.Fprintf(&sb, "## Agent Notes\n\n_To be filled by agent after analysis._\n")

	rel := fmt.Sprintf("experiments/%03d-%s-%s.md", ordinal, task.Model, task.Engine)
	if err := w.writeFactDocument(rel, sb.String()); err != nil {
		return "", err
	}
	return rel, nil
}

func (w *ExplorerWorkspace) allocateExperimentOrdinal(preferred int) (int, error) {
	if preferred <= 0 {
		preferred = 1
	}
	pattern, err := w.safePath("experiments/*.md")
	if err != nil {
		return 0, err
	}
	paths, err := filepath.Glob(pattern)
	if err != nil {
		return 0, fmt.Errorf("glob experiments: %w", err)
	}
	maxOrdinal := 0
	for _, path := range paths {
		name := filepath.Base(path)
		ordinal := leadingExperimentOrdinal(name)
		if ordinal > maxOrdinal {
			maxOrdinal = ordinal
		}
	}
	if maxOrdinal >= preferred {
		return maxOrdinal + 1, nil
	}
	return preferred, nil
}

func leadingExperimentOrdinal(name string) int {
	if name == "" {
		return 0
	}
	end := 0
	for end < len(name) && name[end] >= '0' && name[end] <= '9' {
		end++
	}
	if end == 0 {
		return 0
	}
	n, err := strconv.Atoi(name[:end])
	if err != nil {
		return 0
	}
	return n
}

// ParsePlan reads plan.md and returns the task list.
func (w *ExplorerWorkspace) ParsePlan() ([]TaskSpec, error) {
	md, err := w.ReadFile("plan.md")
	if err != nil {
		return nil, fmt.Errorf("read plan.md: %w", err)
	}
	return parsePlanTasks(md)
}

// ExtractRecommendations reads summary.md and returns the recommended configurations.
// Returns nil, nil if summary.md does not exist yet (normal early-session state).
func (w *ExplorerWorkspace) ExtractRecommendations() ([]RecommendedConfig, error) {
	md, err := w.ReadFile("summary.md")
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("read summary.md: %w", err)
	}
	return parseRecommendedConfigs(md)
}

// extractSection returns the content from a markdown heading until the next
// heading of equal or higher level (or end of document).
func extractSection(md, heading string) string {
	level := len(heading) - len(strings.TrimLeft(heading, "#"))
	idx := strings.Index(md, heading)
	if idx == -1 {
		return ""
	}
	rest := md[idx+len(heading):]
	// Find next heading of same or higher level
	prefix := strings.Repeat("#", level) + " "
	for i := 0; i < len(rest); i++ {
		if i == 0 || rest[i-1] == '\n' {
			remaining := rest[i:]
			if strings.HasPrefix(remaining, prefix) || (level > 1 && strings.HasPrefix(remaining, strings.Repeat("#", level-1)+" ")) {
				return rest[:i]
			}
		}
	}
	return rest
}
