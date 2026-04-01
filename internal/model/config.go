package model

import (
	"strings"

	"github.com/jguan/aima/catalog"
	"gopkg.in/yaml.v3"
)

// ScannerConfig is the top-level YAML structure.
type ScannerConfig struct {
	Kind     string         `yaml:"kind"`
	Metadata map[string]any `yaml:"metadata"`
	Config   Config         `yaml:"config"`
}

// Config contains scanner settings.
type Config struct {
	MaxScanDepth      int                   `yaml:"max_scan_depth"`
	MinModelSizeBytes int64                 `yaml:"min_model_size_bytes"`
	SkipSubdirNames   []string              `yaml:"skip_subdir_names"`
	ParentPatterns    []ConfigParentPattern `yaml:"parent_patterns"`
	ModelPatterns     []ConfigPattern       `yaml:"model_patterns"`
}

// ConfigParentPattern defines parent model detection.
type ConfigParentPattern struct {
	IndicatorFiles         []string `yaml:"indicator_files"`
	IndicatorField         string   `yaml:"indicator_field"`
	IndicatorValueContains string   `yaml:"indicator_value_contains"`
}

// ConfigPattern defines model detection from YAML.
type ConfigPattern struct {
	Name                   string            `yaml:"name"`
	ConfigFiles            []string          `yaml:"config_files"`
	WeightExts             []string          `yaml:"weight_exts"`
	Format                 string            `yaml:"format"`
	TypeHint               string            `yaml:"type_hint"`
	ModelClassHint         string            `yaml:"model_class_hint"`
	IndicatorField         string            `yaml:"indicator_field"`
	IndicatorValueContains string            `yaml:"indicator_value_contains"`
	TaskMappings           map[string]string `yaml:"task_mappings"`     // config "task" substring → model type
	RecursiveWeights       bool              `yaml:"recursive_weights"` // true = search nested component dirs
	SkipMinSize            bool              `yaml:"skip_min_size"`     // true = exempt from min size filter
}

// ParentPattern is internal representation.
type ParentPattern struct {
	IndicatorFiles         []string
	IndicatorField         string
	IndicatorValueContains string
}

// ScanConfig holds scanner configuration loaded from YAML or defaults.
type ScanConfig struct {
	MaxScanDepth    int
	MinModelSize    int64
	SkipSubdirNames map[string]bool // Set for O(1) lookup
	ParentPatterns  []ParentPattern
	ModelPatterns   []ModelPattern
}

// ModelPattern defines how to detect a model format (internal).
type ModelPattern struct {
	name                   string            // Pattern name for debugging
	configFiles            []string          // Possible config filenames (empty = no config needed)
	weightExts             []string          // Possible weight file extensions
	format                 string            // Output format name
	typeHint               string            // Type hint when detectArch fails
	modelClassHint         string            // Model class hint when config metadata is sparse
	indicatorField         string            // Optional config field gate
	indicatorValueContains string            // Optional substring match for indicatorField
	taskMappings           map[string]string // config "task" field substring → model type
	recursiveWeights       bool              // true = search nested component dirs
	skipMinSize            bool              // true = exempt from min size filter
}

// NewScanConfig loads scanner configuration from the embedded catalog YAML.
// Falls back to built-in defaults if the YAML is missing or invalid.
func NewScanConfig() *ScanConfig {
	sc := &ScanConfig{}

	data, err := catalog.FS.ReadFile("scanner.yaml")
	if err != nil {
		sc.applyDefaults()
		return sc
	}

	var yamlConfig ScannerConfig
	if err := yaml.Unmarshal(data, &yamlConfig); err != nil {
		sc.applyDefaults()
		return sc
	}

	if yamlConfig.Kind != "scanner_config" {
		sc.applyDefaults()
		return sc
	}

	cfg := yamlConfig.Config

	if cfg.MaxScanDepth > 0 {
		sc.MaxScanDepth = cfg.MaxScanDepth
	} else {
		sc.MaxScanDepth = 4
	}

	if cfg.MinModelSizeBytes > 0 {
		sc.MinModelSize = cfg.MinModelSizeBytes
	} else {
		sc.MinModelSize = 10 * 1024 * 1024 // 10MB
	}

	sc.SkipSubdirNames = make(map[string]bool)
	for _, name := range cfg.SkipSubdirNames {
		sc.SkipSubdirNames[strings.ToLower(name)] = true
	}

	for _, p := range cfg.ParentPatterns {
		if len(p.IndicatorFiles) == 0 {
			continue
		}
		sc.ParentPatterns = append(sc.ParentPatterns, ParentPattern{
			IndicatorFiles:         p.IndicatorFiles,
			IndicatorField:         p.IndicatorField,
			IndicatorValueContains: p.IndicatorValueContains,
		})
	}

	for _, p := range cfg.ModelPatterns {
		if p.Name == "" || p.Format == "" {
			continue
		}
		sc.ModelPatterns = append(sc.ModelPatterns, ModelPattern{
			name:                   p.Name,
			configFiles:            p.ConfigFiles,
			weightExts:             p.WeightExts,
			format:                 p.Format,
			typeHint:               p.TypeHint,
			modelClassHint:         p.ModelClassHint,
			indicatorField:         p.IndicatorField,
			indicatorValueContains: p.IndicatorValueContains,
			taskMappings:           p.TaskMappings,
			recursiveWeights:       p.RecursiveWeights,
			skipMinSize:            p.SkipMinSize,
		})
	}

	if len(sc.ModelPatterns) == 0 {
		sc.applyDefaultPatterns()
	}

	return sc
}

func (sc *ScanConfig) applyDefaults() {
	sc.MaxScanDepth = 4
	sc.MinModelSize = 10 * 1024 * 1024 // 10MB
	sc.SkipSubdirNames = make(map[string]bool)
	defaultSkipNames := []string{
		"text_encoder", "transformer", "vae", "unet", "controlnet",
		"scheduler", "feature_extractor", "speech_tokenizer", "tokenizer",
		"tokenizer_config", "processor", "gguf-fp16", "gguf-q4", "gguf-q8",
		"fp16", "fp32", "quantized", "mmproj", "encoder", "decoder",
		"postprocessor", "preprocessor", "vision_model", "audio_encoder", "projection",
	}
	for _, name := range defaultSkipNames {
		sc.SkipSubdirNames[name] = true
	}
	sc.ParentPatterns = []ParentPattern{
		{
			IndicatorFiles:         []string{"model_index.json"},
			IndicatorField:         "_class_name",
			IndicatorValueContains: "Pipeline",
		},
	}
	sc.applyDefaultPatterns()
}

func (sc *ScanConfig) applyDefaultPatterns() {
	sc.ModelPatterns = []ModelPattern{
		{
			name:                   "diffusers_pipeline",
			configFiles:            []string{"model_index.json"},
			weightExts:             []string{".safetensors"},
			format:                 "safetensors",
			typeHint:               "image_gen",
			modelClassHint:         "diffusion",
			indicatorField:         "_class_name",
			indicatorValueContains: "Pipeline",
			recursiveWeights:       true,
		},
		{
			name:        "huggingface_safetensors",
			configFiles: []string{"config.json"},
			weightExts:  []string{".safetensors"},
			format:      "safetensors",
		},
		{
			name:        "huggingface_pytorch",
			configFiles: []string{"config.json", "configuration.json"},
			weightExts:  []string{"pytorch_model.bin", ".bin"},
			format:      "pytorch",
		},
		{
			name:        "pytorch_pt",
			configFiles: []string{"config.json", "configuration.json"},
			weightExts:  []string{".pt"},
			format:      "pytorch",
		},
		{
			name:        "pytorch_pth",
			configFiles: []string{"config.json", "configuration.json"},
			weightExts:  []string{".pth"},
			format:      "pytorch",
		},
		{
			name:        "funasr",
			configFiles: []string{"configuration.json"},
			weightExts:  []string{".pt"},
			format:      "pytorch",
			typeHint:    "asr",
		},
		{
			name:        "funasr_onnx",
			configFiles: []string{"configuration.json"},
			weightExts:  []string{".onnx"},
			format:      "onnx",
			skipMinSize: true,
			taskMappings: map[string]string{
				"speech-recognition":       "asr",
				"voice-activity-detection": "vad",
				"text-to-speech":           "tts",
				"punctuation":              "nlp",
			},
		},
		{
			name:        "onnx",
			configFiles: []string{"config.json", "config.yaml"},
			weightExts:  []string{".onnx"},
			format:      "onnx",
			skipMinSize: true,
		},
		{
			name:        "mnn",
			configFiles: []string{},
			weightExts:  []string{".mnn"},
			format:      "mnn",
			skipMinSize: true,
		},
		{
			name:        "gguf",
			configFiles: []string{},
			weightExts:  []string{".gguf"},
			format:      "gguf",
			typeHint:    "llm",
		},
	}
}
