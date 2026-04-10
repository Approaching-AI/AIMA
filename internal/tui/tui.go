package tui

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

const (
	tabDashboard = 0
	tabDeploys   = 1
	tabMetrics   = 2
)

var (
	tabNames = []string{"Dashboard", "Deployments", "Metrics"}

	titleStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("12"))

	activeTabStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("0")).
			Background(lipgloss.Color("12")).
			Padding(0, 1)

	inactiveTabStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("8")).
				Padding(0, 1)

	boxStyle = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(lipgloss.Color("8")).
			Padding(0, 1)

	warnStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("11"))

	okStyle = lipgloss.NewStyle().
		Foreground(lipgloss.Color("10"))

	errStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("9"))

	dimStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("8"))
)

// Model is the main TUI model.
type Model struct {
	endpoint  string
	activeTab int
	width     int
	height    int

	// Data
	device  map[string]any
	deploys []map[string]any
	metrics map[string]any
	err     error
}

type tickMsg time.Time
type dataMsg struct {
	device  map[string]any
	deploys []map[string]any
	metrics map[string]any
	err     error
}

// NewModel creates a TUI model that polls the given AIMA endpoint.
func NewModel(endpoint string) Model {
	return Model{
		endpoint: endpoint,
	}
}

func (m Model) Init() tea.Cmd {
	return tea.Batch(
		m.fetchData(),
		tickCmd(),
	)
}

func tickCmd() tea.Cmd {
	return tea.Tick(2*time.Second, func(t time.Time) tea.Msg {
		return tickMsg(t)
	})
}

func (m Model) fetchData() tea.Cmd {
	return func() tea.Msg {
		msg := dataMsg{}

		// Fetch device info
		if data, err := httpGet(m.endpoint + "/api/v1/status"); err == nil {
			_ = json.Unmarshal(data, &msg.device)
		}

		// Fetch deployments
		if data, err := httpGet(m.endpoint + "/api/v1/deployments"); err == nil {
			_ = json.Unmarshal(data, &msg.deploys)
		}

		// Fetch metrics (GPU)
		if data, err := httpGet(m.endpoint + "/api/v1/power"); err == nil {
			_ = json.Unmarshal(data, &msg.metrics)
		} else {
			msg.err = err
		}

		return msg
	}
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "q", "ctrl+c":
			return m, tea.Quit
		case "tab", "right", "l":
			m.activeTab = (m.activeTab + 1) % len(tabNames)
		case "shift+tab", "left", "h":
			m.activeTab = (m.activeTab - 1 + len(tabNames)) % len(tabNames)
		case "1":
			m.activeTab = tabDashboard
		case "2":
			m.activeTab = tabDeploys
		case "3":
			m.activeTab = tabMetrics
		}
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
	case tickMsg:
		return m, tea.Batch(m.fetchData(), tickCmd())
	case dataMsg:
		if msg.device != nil {
			m.device = msg.device
		}
		if msg.deploys != nil {
			m.deploys = msg.deploys
		}
		if msg.metrics != nil {
			m.metrics = msg.metrics
		}
		m.err = msg.err
	}
	return m, nil
}

func (m Model) View() string {
	if m.width == 0 {
		return "Loading..."
	}

	var b strings.Builder

	// Header
	b.WriteString(titleStyle.Render("AIMA Dashboard"))
	b.WriteString("  ")
	b.WriteString(dimStyle.Render(m.endpoint))
	b.WriteString("\n")

	// Tabs
	for i, name := range tabNames {
		if i == m.activeTab {
			b.WriteString(activeTabStyle.Render(fmt.Sprintf(" %d %s ", i+1, name)))
		} else {
			b.WriteString(inactiveTabStyle.Render(fmt.Sprintf(" %d %s ", i+1, name)))
		}
	}
	b.WriteString("\n\n")

	// Content
	contentWidth := m.width - 4
	if contentWidth < 40 {
		contentWidth = 40
	}

	switch m.activeTab {
	case tabDashboard:
		b.WriteString(m.viewDashboard(contentWidth))
	case tabDeploys:
		b.WriteString(m.viewDeploys(contentWidth))
	case tabMetrics:
		b.WriteString(m.viewMetrics(contentWidth))
	}

	// Footer
	b.WriteString("\n")
	b.WriteString(dimStyle.Render("Tab/←→: switch views • 1-3: jump to view • q: quit"))

	return b.String()
}

func (m Model) viewDashboard(width int) string {
	var b strings.Builder

	if m.device == nil {
		b.WriteString(warnStyle.Render("Connecting to " + m.endpoint + "..."))
		return b.String()
	}

	// Device info box
	deviceLines := []string{}
	if hostname, ok := jsonStr(m.device, "hostname"); ok {
		deviceLines = append(deviceLines, fmt.Sprintf("Host: %s", hostname))
	}
	if os, ok := jsonStr(m.device, "os"); ok {
		deviceLines = append(deviceLines, fmt.Sprintf("OS:   %s", os))
	}
	if arch, ok := jsonStr(m.device, "arch"); ok {
		deviceLines = append(deviceLines, fmt.Sprintf("Arch: %s", arch))
	}
	if ver, ok := jsonStr(m.device, "version"); ok {
		deviceLines = append(deviceLines, fmt.Sprintf("AIMA: %s", ver))
	}

	halfWidth := width/2 - 2
	if halfWidth < 20 {
		halfWidth = 20
	}
	deviceBox := boxStyle.Width(halfWidth).Render(
		titleStyle.Render("Device") + "\n" + strings.Join(deviceLines, "\n"))

	// GPU info box
	gpuLines := m.renderGPUInfo()
	gpuBox := boxStyle.Width(halfWidth).Render(
		titleStyle.Render("GPU") + "\n" + strings.Join(gpuLines, "\n"))

	b.WriteString(lipgloss.JoinHorizontal(lipgloss.Top, deviceBox, " ", gpuBox))
	b.WriteString("\n\n")

	// Deploy count
	deployCount := len(m.deploys)
	deployStatus := okStyle.Render(fmt.Sprintf("%d active", deployCount))
	if deployCount == 0 {
		deployStatus = dimStyle.Render("none")
	}
	b.WriteString(fmt.Sprintf("Deployments: %s", deployStatus))

	return b.String()
}

func (m Model) renderGPUInfo() []string {
	if m.metrics == nil {
		return []string{dimStyle.Render("No GPU data")}
	}

	var lines []string
	if name, ok := jsonStr(m.metrics, "gpu_name"); ok {
		lines = append(lines, name)
	}

	if utilPct, ok := jsonFloat(m.metrics, "utilization_pct"); ok {
		lines = append(lines, fmt.Sprintf("Util: %s %3.0f%%", progressBar(utilPct, 20), utilPct))
	}
	if vramUsed, ok := jsonFloat(m.metrics, "vram_used_mib"); ok {
		if vramTotal, ok2 := jsonFloat(m.metrics, "vram_total_mib"); ok2 && vramTotal > 0 {
			pct := vramUsed / vramTotal * 100
			lines = append(lines, fmt.Sprintf("VRAM: %s %3.0f%% (%d/%d MiB)",
				progressBar(pct, 20), pct, int(vramUsed), int(vramTotal)))
		}
	}
	if temp, ok := jsonFloat(m.metrics, "temperature_c"); ok {
		style := okStyle
		if temp > 80 {
			style = errStyle
		} else if temp > 70 {
			style = warnStyle
		}
		lines = append(lines, fmt.Sprintf("Temp: %s", style.Render(fmt.Sprintf("%.0f°C", temp))))
	}
	if power, ok := jsonFloat(m.metrics, "power_draw_watts"); ok {
		lines = append(lines, fmt.Sprintf("Power: %.0fW", power))
	}

	return lines
}

func (m Model) viewDeploys(width int) string {
	if len(m.deploys) == 0 {
		return dimStyle.Render("No active deployments")
	}

	var b strings.Builder
	b.WriteString(fmt.Sprintf("%-20s %-15s %-15s %-10s\n",
		dimStyle.Render("NAME"), dimStyle.Render("MODEL"), dimStyle.Render("ENGINE"), dimStyle.Render("STATUS")))
	b.WriteString(strings.Repeat("─", min(width, 65)) + "\n")

	for _, d := range m.deploys {
		name, _ := jsonStr(d, "name")
		model, _ := jsonStr(d, "model")
		engine, _ := jsonStr(d, "engine")
		status, _ := jsonStr(d, "status")
		if status == "" {
			status, _ = jsonStr(d, "phase")
		}

		statusStyle := dimStyle
		switch strings.ToLower(status) {
		case "running":
			statusStyle = okStyle
		case "crashloopbackoff", "error", "failed":
			statusStyle = errStyle
		case "pending", "containercreating", "starting":
			statusStyle = warnStyle
		}

		b.WriteString(fmt.Sprintf("%-20s %-15s %-15s %s\n",
			truncate(name, 20), truncate(model, 15), truncate(engine, 15),
			statusStyle.Render(truncate(status, 10))))
	}
	return b.String()
}

func (m Model) viewMetrics(width int) string {
	if m.metrics == nil {
		return dimStyle.Render("No metrics data")
	}

	var b strings.Builder
	b.WriteString(titleStyle.Render("GPU Metrics") + "\n\n")

	fields := []struct{ key, label string }{
		{"gpu_name", "GPU"},
		{"utilization_pct", "Utilization"},
		{"temperature_c", "Temperature"},
		{"power_draw_watts", "Power Draw"},
		{"vram_used_mib", "VRAM Used"},
		{"vram_total_mib", "VRAM Total"},
	}

	for _, f := range fields {
		if v, ok := m.metrics[f.key]; ok {
			b.WriteString(fmt.Sprintf("  %-15s %v\n", f.label+":", v))
		}
	}

	b.WriteString("\n")
	b.WriteString(dimStyle.Render("Updates every 2 seconds"))
	return b.String()
}

// Helpers

func progressBar(pct float64, width int) string {
	if pct < 0 {
		pct = 0
	}
	if pct > 100 {
		pct = 100
	}
	filled := int(pct / 100 * float64(width))
	empty := width - filled

	color := "10" // green
	if pct > 90 {
		color = "9" // red
	} else if pct > 70 {
		color = "11" // yellow
	}

	return lipgloss.NewStyle().Foreground(lipgloss.Color(color)).Render(strings.Repeat("█", filled)) +
		dimStyle.Render(strings.Repeat("░", empty))
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	if n <= 3 {
		return s[:n]
	}
	return s[:n-1] + "…"
}

func jsonStr(m map[string]any, key string) (string, bool) {
	v, ok := m[key]
	if !ok {
		return "", false
	}
	s, ok := v.(string)
	return s, ok
}

func jsonFloat(m map[string]any, key string) (float64, bool) {
	v, ok := m[key]
	if !ok {
		return 0, false
	}
	switch n := v.(type) {
	case float64:
		return n, true
	case int:
		return float64(n), true
	case json.Number:
		f, err := n.Float64()
		return f, err == nil
	}
	return 0, false
}

func httpGet(url string) ([]byte, error) {
	client := &http.Client{Timeout: 3 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	return io.ReadAll(resp.Body)
}
