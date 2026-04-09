package agent

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// readOnlyDocs lists AIMA-generated fact documents the Explorer agent must not overwrite.
var readOnlyDocs = map[string]bool{
	"device-profile.md":  true,
	"available-combos.md": true,
	"knowledge-base.md":  true,
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
func parsePlanTasks(md string) ([]TaskSpec, error) {
	section := extractSection(md, "## Tasks")
	if section == "" {
		return nil, fmt.Errorf("no ## Tasks section found")
	}
	matches := yamlBlockRe.FindStringSubmatch(section)
	if len(matches) < 2 {
		return nil, fmt.Errorf("no yaml code block in ## Tasks section")
	}
	var tasks []TaskSpec
	if err := yaml.Unmarshal([]byte(matches[1]), &tasks); err != nil {
		return nil, fmt.Errorf("parse tasks yaml: %w", err)
	}
	return tasks, nil
}

// parseRecommendedConfigs extracts RecommendedConfig list from summary.md.
// Looks for the yaml code block under "## Recommended Configurations".
func parseRecommendedConfigs(md string) ([]RecommendedConfig, error) {
	section := extractSection(md, "## Recommended Configurations")
	if section == "" {
		return nil, nil // no recommendations yet is normal
	}
	matches := yamlBlockRe.FindStringSubmatch(section)
	if len(matches) < 2 {
		return nil, nil
	}
	var configs []RecommendedConfig
	if err := yaml.Unmarshal([]byte(matches[1]), &configs); err != nil {
		return nil, fmt.Errorf("parse recommendations yaml: %w", err)
	}
	return configs, nil
}

// RefreshFactDocuments regenerates all AIMA fact documents from current PlanInput.
// Uses writeFactDocument to bypass the read-only guard (these are AIMA-owned files).
func (w *ExplorerWorkspace) RefreshFactDocuments(input PlanInput) error {
	now := time.Now().Format("2006-01-02 15:04:05")
	docs := map[string]string{
		"device-profile.md":  w.generateDeviceProfile(input, now),
		"available-combos.md": w.generateAvailableCombos(input, now),
		"knowledge-base.md":  w.generateKnowledgeBase(input, now),
	}
	for name, content := range docs {
		if err := w.writeFactDocument(name, content); err != nil {
			return fmt.Errorf("refresh %s: %w", name, err)
		}
	}
	return nil
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
	fmt.Fprintf(&sb, "| Name | Format | Type | Size (GiB) | Fits VRAM |\n")
	fmt.Fprintf(&sb, "|------|--------|------|------------|----------|\n")
	for _, m := range input.LocalModels {
		sizeGiB := float64(m.SizeBytes) / (1024 * 1024 * 1024)
		fits := "✅"
		reason := ""
		if !modelFitsVRAM(m.Name, input.LocalModels, totalVRAM) {
			fits = "❌"
			reason = " (VRAM overflow)"
		}
		fmt.Fprintf(&sb, "| %s | %s | %s | %.2f | %s%s |\n", m.Name, m.Format, m.Type, sizeGiB, fits, reason)
	}
	fmt.Fprintf(&sb, "\n")

	// Engines table
	fmt.Fprintf(&sb, "## Local Engines\n\n")
	fmt.Fprintf(&sb, "| Type | Runtime | Features | Tunable Params |\n")
	fmt.Fprintf(&sb, "|------|---------|----------|----------------|\n")
	for _, e := range input.LocalEngines {
		features := strings.Join(e.Features, ", ")
		paramKeys := make([]string, 0, len(e.TunableParams))
		for k := range e.TunableParams {
			paramKeys = append(paramKeys, k)
		}
		params := strings.Join(paramKeys, ", ")
		fmt.Fprintf(&sb, "| %s | %s | %s | %s |\n", e.Type, e.Runtime, features, params)
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
			} else if !engineSupportsModelType(e.Type, m.Type) {
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

	fmt.Fprintf(&sb, "## Unexplored\n\n")
	if len(unexplored) == 0 {
		fmt.Fprintf(&sb, "_None_\n\n")
	} else {
		fmt.Fprintf(&sb, "| Model | Engine |\n|-------|--------|\n")
		for _, r := range unexplored {
			fmt.Fprintf(&sb, "| %s | %s |\n", r.model, r.engine)
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

	fmt.Fprintf(&sb, "## Incompatible\n\n")
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
