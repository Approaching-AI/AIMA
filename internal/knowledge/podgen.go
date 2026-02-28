package knowledge

import (
	"bytes"
	"fmt"
	"path/filepath"
	"sort"
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
  schedulerName: default-scheduler
  restartPolicy: Always
  {{- if .RuntimeClassName }}
  runtimeClassName: {{ .RuntimeClassName }}
  {{- end }}
  containers:
    - name: inference
      image: {{ .EngineImage }}
      {{- if .Args }}
      command:
        {{- range .Args }}
        - "{{ . }}"
        {{- end }}
      {{- end }}
      ports:
        - containerPort: {{ .Port }}
          name: http
      {{- if or .RuntimeClassName .ExtraEnv }}
      env:
        {{- if .RuntimeClassName }}
        - name: NVIDIA_VISIBLE_DEVICES
          value: all
        - name: NVIDIA_DRIVER_CAPABILITIES
          value: all
        - name: LD_LIBRARY_PATH
          value: /lib/x86_64-linux-gnu:/usr/local/nvidia/lib:/usr/local/nvidia/lib64
        {{- end }}
        {{- range $k, $v := .ExtraEnv }}
        - name: {{ $k }}
          value: "{{ $v }}"
        {{- end }}
      {{- end }}
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
        initialDelaySeconds: {{ .HealthCheckInitDelaySec }}
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
        - name: dshm
          mountPath: /dev/shm
  volumes:
    - name: model-data
      hostPath:
        path: {{ .ModelHostPath }}
        type: DirectoryOrCreate
    - name: dshm
      emptyDir:
        medium: Memory
`))

type podData struct {
	PodName          string
	Engine           string
	EngineImage      string
	ModelName        string
	Slot             string
	Port             int
	Args             []string // command arguments (excluding binary name — image entrypoint is used)
	ExtraEnv         map[string]string // additional env vars from engine YAML
	GPUMemoryMiB     int
	GPUCoresPercent  int
	CPUCores         int
	RAMMiB           int
	HealthCheckPath        string
	HealthCheckInitDelaySec int // initialDelaySeconds for liveness probe — use health_check.timeout_s
	ModelHostPath          string
	GPUResourceName        string
	RuntimeClassName       string // e.g. "nvidia" for NVIDIA CUDA containers
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

	// Process command: replace {{.ModelPath}} template.
	// Use K8s command: (not args:) so we override the container ENTRYPOINT entirely.
	// This is required for NGC images that wrap their entrypoint in a shell script
	// (e.g. nvcr.io/nvidia/vllm uses /opt/nvidia/nvidia_entrypoint.sh as ENTRYPOINT,
	// so args alone would be passed to the shell, not to vllm directly).
	args := make([]string, len(resolved.Command))
	for i, c := range resolved.Command {
		args[i] = strings.ReplaceAll(c, "{{.ModelPath}}", containerModelPath)
	}

	// Append resolved config values as CLI flags.
	// Config keys use underscore (e.g. "gpu_memory_utilization") → "--gpu-memory-utilization".
	// "port" is excluded: it is mapped to containerPort in the pod spec.
	if len(resolved.Config) > 0 {
		keys := make([]string, 0, len(resolved.Config))
		for k := range resolved.Config {
			if k != "port" && k != "model_path" {
				keys = append(keys, k)
			}
		}
		sort.Strings(keys) // deterministic ordering for reproducible pod specs
		for _, k := range keys {
			flagName := strings.ReplaceAll(k, "_", "-")
			switch v := resolved.Config[k].(type) {
			case bool:
				if v {
					args = append(args, "--"+flagName)
				} else {
					args = append(args, "--no-"+flagName)
				}
			default:
				args = append(args, "--"+flagName, fmt.Sprintf("%v", v))
			}
		}
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
		ExtraEnv:         resolved.Env,
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
		if resolved.HealthCheck.TimeoutS > 0 {
			data.HealthCheckInitDelaySec = resolved.HealthCheck.TimeoutS
		} else {
			data.HealthCheckInitDelaySec = 300
		}
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

// SanitizePodName is the exported version for use by other packages.
func SanitizePodName(name string) string { return sanitizePodName(name) }

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
