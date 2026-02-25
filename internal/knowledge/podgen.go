package knowledge

import (
	"bytes"
	"fmt"
	"strings"
	"text/template"

	"gopkg.in/yaml.v3"
)

var podTemplate = template.Must(template.New("pod").Parse(`apiVersion: v1
kind: Pod
metadata:
  name: {{ .PodName }}
  labels:
    aima.dev/engine: "{{ .Engine }}"
    aima.dev/model: "{{ .ModelName }}"
    aima.dev/slot: "{{ .Slot }}"
    app: aima-inference
  {{- if .HasAnnotations }}
  annotations:
    {{- if gt .GPUMemoryMiB 0 }}
    nvidia.com/gpumem: "{{ .GPUMemoryMiB }}"
    {{- end }}
    {{- if gt .GPUCoresPercent 0 }}
    nvidia.com/gpucores: "{{ .GPUCoresPercent }}"
    {{- end }}
  {{- end }}
spec:
  restartPolicy: Always
  containers:
    - name: inference
      image: {{ .EngineImage }}
      {{- if .Command }}
      command:
        {{- range .Command }}
        - "{{ . }}"
        {{- end }}
      {{- end }}
      ports:
        - containerPort: {{ .Port }}
          name: http
      {{- if .HasResources }}
      resources:
        limits:
          nvidia.com/gpu: "1"
          {{- if gt .CPUCores 0 }}
          cpu: "{{ .CPUCores }}"
          {{- end }}
          {{- if gt .RAMMiB 0 }}
          memory: "{{ .RAMMiB }}Mi"
          {{- end }}
        requests:
          nvidia.com/gpu: "1"
      {{- end }}
      {{- if .HealthCheckPath }}
      livenessProbe:
        httpGet:
          path: {{ .HealthCheckPath }}
          port: {{ .Port }}
        initialDelaySeconds: 30
        periodSeconds: 10
        timeoutSeconds: 5
        failureThreshold: 3
      readinessProbe:
        httpGet:
          path: {{ .HealthCheckPath }}
          port: {{ .Port }}
        initialDelaySeconds: 10
        periodSeconds: 5
        timeoutSeconds: 3
        failureThreshold: 3
      {{- end }}
      volumeMounts:
        - name: model-data
          mountPath: /models
          readOnly: true
  volumes:
    - name: model-data
      hostPath:
        path: {{ .ModelHostPath }}
        type: DirectoryOrCreate
`))

type podData struct {
	PodName        string
	Engine         string
	EngineImage    string
	ModelName      string
	Slot           string
	Port           int
	Command        []string
	GPUMemoryMiB   int
	GPUCoresPercent int
	CPUCores       int
	RAMMiB         int
	HealthCheckPath string
	ModelHostPath  string
}

func (d podData) HasAnnotations() bool {
	return d.GPUMemoryMiB > 0 || d.GPUCoresPercent > 0
}

func (d podData) HasResources() bool {
	return d.GPUMemoryMiB > 0 || d.GPUCoresPercent > 0 || d.CPUCores > 0 || d.RAMMiB > 0
}

// GeneratePod generates K3S Pod YAML from a resolved configuration.
func GeneratePod(resolved *ResolvedConfig) ([]byte, error) {
	if resolved == nil {
		return nil, fmt.Errorf("generate pod: resolved config is nil")
	}

	port := 8000
	if p, ok := resolved.Config["port"]; ok {
		switch v := p.(type) {
		case int:
			port = v
		case float64:
			port = int(v)
		}
	}

	modelHostPath := resolved.ModelPath
	if modelHostPath == "" {
		modelHostPath = "/data/models/" + resolved.ModelName
	}

	// Process command: replace {{.ModelPath}} template
	command := make([]string, len(resolved.Command))
	for i, c := range resolved.Command {
		command[i] = strings.ReplaceAll(c, "{{.ModelPath}}", "/models")
	}

	data := podData{
		PodName:     sanitizePodName(resolved.ModelName + "-" + resolved.Engine),
		Engine:      resolved.Engine,
		EngineImage: resolved.EngineImage,
		ModelName:   resolved.ModelName,
		Slot:        resolved.Slot,
		Port:        port,
		Command:     command,
		ModelHostPath: modelHostPath,
	}

	if resolved.Partition != nil {
		data.GPUMemoryMiB = resolved.Partition.GPUMemoryMiB
		data.GPUCoresPercent = resolved.Partition.GPUCoresPercent
		data.CPUCores = resolved.Partition.CPUCores
		data.RAMMiB = resolved.Partition.RAMMiB
	}

	if resolved.HealthCheck != nil {
		data.HealthCheckPath = resolved.HealthCheck.Path
	}

	var buf bytes.Buffer
	if err := podTemplate.Execute(&buf, data); err != nil {
		return nil, fmt.Errorf("render pod template: %w", err)
	}

	// Validate the generated YAML
	var check map[string]any
	if err := yaml.Unmarshal(buf.Bytes(), &check); err != nil {
		return nil, fmt.Errorf("generated pod YAML is invalid: %w", err)
	}

	return buf.Bytes(), nil
}

func sanitizePodName(name string) string {
	// K8s pod names: lowercase, alphanumeric, dashes, max 253 chars
	name = strings.ToLower(name)
	var b strings.Builder
	for _, r := range name {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' {
			b.WriteRune(r)
		} else if r == '_' || r == '.' || r == ' ' {
			b.WriteByte('-')
		}
	}
	result := b.String()
	// Trim leading/trailing dashes
	result = strings.Trim(result, "-")
	if len(result) > 253 {
		result = result[:253]
	}
	if result == "" {
		result = "aima-inference"
	}
	return result
}
