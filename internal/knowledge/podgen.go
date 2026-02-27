package knowledge

import (
	"bytes"
	"fmt"
	"path/filepath"
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
    {{ .GPUVendorDomain }}/gpumem: "{{ .GPUMemoryMiB }}"
    {{- end }}
    {{- if gt .GPUCoresPercent 0 }}
    {{ .GPUVendorDomain }}/gpucores: "{{ .GPUCoresPercent }}"
    {{- end }}
  {{- end }}
spec:
  restartPolicy: Always
  {{- if .RuntimeClassName }}
  runtimeClassName: {{ .RuntimeClassName }}
  {{- end }}
  containers:
    - name: inference
      image: {{ .EngineImage }}
      {{- if .Args }}
      args:
        {{- range .Args }}
        - "{{ . }}"
        {{- end }}
      {{- end }}
      ports:
        - containerPort: {{ .Port }}
          name: http
      {{- if or .HasGPUResource .HasComputeResources }}
      resources:
        limits:
          {{- if .HasGPUResource }}
          {{ .GPUResourceName }}: "1"
          {{- end }}
          {{- if gt .CPUCores 0 }}
          cpu: "{{ .CPUCores }}"
          {{- end }}
          {{- if gt .RAMMiB 0 }}
          memory: "{{ .RAMMiB }}Mi"
          {{- end }}
        {{- if .HasGPUResource }}
        requests:
          {{ .GPUResourceName }}: "1"
        {{- end }}
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
	PodName          string
	Engine           string
	EngineImage      string
	ModelName        string
	Slot             string
	Port             int
	Args             []string // command arguments (excluding binary name — image entrypoint is used)
	GPUMemoryMiB     int
	GPUCoresPercent  int
	CPUCores         int
	RAMMiB           int
	HealthCheckPath  string
	ModelHostPath    string
	GPUResourceName  string
	RuntimeClassName string // e.g. "nvidia" for NVIDIA CUDA containers
}

func (d podData) HasAnnotations() bool {
	return d.GPUMemoryMiB > 0 || d.GPUCoresPercent > 0
}

// HasGPUResource reports whether a device-plugin GPU resource request should be added.
// True only when there is explicit GPU partitioning (HAMi-style); false when using
// runtimeClassName for GPU access without a device plugin.
func (d podData) HasGPUResource() bool {
	return d.GPUMemoryMiB > 0 || d.GPUCoresPercent > 0
}

// HasComputeResources reports whether CPU or RAM limits should be set.
func (d podData) HasComputeResources() bool {
	return d.CPUCores > 0 || d.RAMMiB > 0
}

// GPUVendorDomain extracts the vendor domain from the GPU resource name.
// e.g. "nvidia.com/gpu" -> "nvidia.com", "amd.com/gpu" -> "amd.com"
func (d podData) GPUVendorDomain() string {
	if i := strings.LastIndex(d.GPUResourceName, "/"); i > 0 {
		return d.GPUResourceName[:i]
	}
	return d.GPUResourceName
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

	// containerModelPath is the path passed to the engine command inside the pod.
	// If modelHostPath points to a specific file (e.g. a .gguf), mount its parent
	// directory so type:DirectoryOrCreate works, and point the command at the file.
	containerModelPath := "/models"
	if isModelFilePath(modelHostPath) {
		containerModelPath = "/models/" + filepath.Base(modelHostPath)
		modelHostPath = filepath.Dir(modelHostPath)
	}

	// Process command: replace {{.ModelPath}} template, then extract args (skip binary name).
	// K8s pods use the image's own ENTRYPOINT; we only pass args to avoid path issues.
	command := make([]string, len(resolved.Command))
	for i, c := range resolved.Command {
		command[i] = strings.ReplaceAll(c, "{{.ModelPath}}", containerModelPath)
	}
	args := command
	if len(command) > 1 {
		args = command[1:]
	}

	gpuResource := resolved.GPUResourceName
	if gpuResource == "" {
		gpuResource = "nvidia.com/gpu"
	}

	data := podData{
		PodName:          sanitizePodName(resolved.ModelName + "-" + resolved.Engine),
		Engine:           resolved.Engine,
		EngineImage:      resolved.EngineImage,
		ModelName:        resolved.ModelName,
		Slot:             resolved.Slot,
		Port:             port,
		Args:             args,
		ModelHostPath:    modelHostPath,
		GPUResourceName:  gpuResource,
		RuntimeClassName: resolved.RuntimeClassName,
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

// isModelFilePath reports whether path points to a model file (not a directory).
// Recognized by common model file extensions.
func isModelFilePath(path string) bool {
	ext := strings.ToLower(filepath.Ext(path))
	switch ext {
	case ".gguf", ".ggml", ".bin", ".safetensors":
		return true
	}
	return false
}
