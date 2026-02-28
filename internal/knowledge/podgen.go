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

var podTemplate = template.Must(template.New("pod").Funcs(template.FuncMap{
	"deviceVolName": deviceVolName,
}).Parse(`apiVersion: v1
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
  {{- if .HasSecurityContext }}
  securityContext:
    {{- if .Security.SupplementalGroups }}
    supplementalGroups:
      {{- range .Security.SupplementalGroups }}
      - {{ . }}
      {{- end }}
    {{- end }}
  {{- end }}
  containers:
    - name: inference
      image: {{ .EngineImage }}
      imagePullPolicy: IfNotPresent
      {{- if .Args }}
      command:
        {{- range .Args }}
        - "{{ . }}"
        {{- end }}
      {{- end }}
      ports:
        - containerPort: {{ .Port }}
          name: http
      {{- if .ExtraEnv }}
      env:
        {{- range $k, $v := .ExtraEnv }}
        - name: {{ $k }}
          value: "{{ $v }}"
        {{- end }}
      {{- end }}
      {{- if .HasContainerSecurity }}
      securityContext:
        privileged: true
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
        {{- range .Devices }}
        - name: {{ deviceVolName . }}
          mountPath: {{ . }}
        {{- end }}
        {{- range .ExtraVolumes }}
        - name: {{ .Name }}
          mountPath: {{ .MountPath }}
          {{- if .ReadOnly }}
          readOnly: true
          {{- end }}
        {{- end }}
  volumes:
    - name: model-data
      hostPath:
        path: {{ .ModelHostPath }}
        type: DirectoryOrCreate
    - name: dshm
      emptyDir:
        medium: Memory
    {{- range .Devices }}
    - name: {{ deviceVolName . }}
      hostPath:
        path: {{ . }}
    {{- end }}
    {{- range .ExtraVolumes }}
    - name: {{ .Name }}
      hostPath:
        path: {{ .HostPath }}
    {{- end }}
`))

// deviceVolName converts a device path like "/dev/kfd" to a K8s-safe volume name like "dev-kfd".
func deviceVolName(path string) string {
	name := strings.TrimPrefix(path, "/")
	name = strings.ReplaceAll(name, "/", "-")
	return name
}

type podData struct {
	PodName          string
	Engine           string
	EngineImage      string
	ModelName        string
	Slot             string
	Port             int
	Args             []string          // command arguments
	ExtraEnv         map[string]string // merged: hardware container env (base) + engine env (override)
	GPUMemoryMiB     int
	GPUCoresPercent  int
	CPUCores         int
	RAMMiB           int
	HealthCheckPath        string
	HealthCheckInitDelaySec int
	ModelHostPath          string
	GPUResourceName        string
	RuntimeClassName       string
	Devices                []string           // device paths to mount, e.g. ["/dev/kfd", "/dev/dri"]
	ExtraVolumes           []ContainerVolume  // additional host mounts
	Security               *ContainerSecurity // pod-level securityContext
}

func (d podData) HasAnnotations() bool {
	return d.GPUResourceName != "" && (d.GPUMemoryMiB > 0 || d.GPUCoresPercent > 0)
}

// HasGPUResource reports whether a device-plugin GPU resource request should be added.
// True only when there is a GPU resource name AND explicit GPU partitioning (HAMi-style).
func (d podData) HasGPUResource() bool {
	return d.GPUResourceName != "" && (d.GPUMemoryMiB > 0 || d.GPUCoresPercent > 0)
}

// HasComputeResources reports whether CPU or RAM limits should be set.
func (d podData) HasComputeResources() bool {
	return d.CPUCores > 0 || d.RAMMiB > 0
}

// HasSecurityContext reports whether pod-level securityContext should be rendered.
func (d podData) HasSecurityContext() bool {
	return d.Security != nil && len(d.Security.SupplementalGroups) > 0
}

// HasContainerSecurity reports whether container-level securityContext (privileged) should be rendered.
func (d podData) HasContainerSecurity() bool {
	return d.Security != nil && d.Security.Privileged
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
	// String values support {{.ModelName}} and {{.ModelPath}} templates.
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
			case string:
				expanded := strings.ReplaceAll(v, "{{.ModelName}}", resolved.ModelName)
				expanded = strings.ReplaceAll(expanded, "{{.ModelPath}}", containerModelPath)
				args = append(args, "--"+flagName, expanded)
			default:
				args = append(args, "--"+flagName, fmt.Sprintf("%v", v))
			}
		}
	}

	// Merge env: hardware container env (base) + engine env (override on conflict).
	mergedEnv := mergeEnv(resolved.Container, resolved.Env)

	data := podData{
		PodName:          sanitizePodName(resolved.ModelName + "-" + resolved.Engine),
		Engine:           resolved.Engine,
		EngineImage:      resolved.EngineImage,
		ModelName:        resolved.ModelName,
		Slot:             resolved.Slot,
		Port:             port,
		Args:             args,
		ExtraEnv:         mergedEnv,
		ModelHostPath:    modelHostPath,
		GPUResourceName:  resolved.GPUResourceName,
		RuntimeClassName: resolved.RuntimeClassName,
	}

	// Populate vendor-specific container access fields from hardware profile.
	if resolved.Container != nil {
		data.Devices = resolved.Container.Devices
		data.ExtraVolumes = resolved.Container.Volumes
		data.Security = resolved.Container.Security
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

// mergeEnv merges hardware container env (base) with engine env (override).
// Engine env wins on conflict. Returns nil if both are empty.
func mergeEnv(container *ContainerAccess, engineEnv map[string]string) map[string]string {
	hwEnv := 0
	if container != nil {
		hwEnv = len(container.Env)
	}
	if hwEnv == 0 && len(engineEnv) == 0 {
		return nil
	}
	merged := make(map[string]string, hwEnv+len(engineEnv))
	if container != nil {
		for k, v := range container.Env {
			merged[k] = v
		}
	}
	// Engine env overrides hardware env on conflict
	for k, v := range engineEnv {
		merged[k] = v
	}
	return merged
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
