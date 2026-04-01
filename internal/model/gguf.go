package model

import (
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// detectGGUFModels detects all GGUF models in a directory.
// GGUF models don't have config.json, so we detect one model per .gguf file.
// Each GGUF file gets its own Path (file path, not directory) for uniqueness.
func detectGGUFModels(dir string, entries []os.DirEntry, p ModelPattern, minSize int64) []*ModelInfo {
	weightFiles := findAllWeightFiles(dir, entries, p.weightExts)
	if len(weightFiles) == 0 {
		return nil
	}

	var models []*ModelInfo
	for _, weightPath := range weightFiles {
		// Check individual file size against minimum
		info, err := os.Stat(weightPath)
		if err != nil {
			continue
		}
		if info.Size() < minSize {
			continue
		}

		// Use the file path as the model path (unique per GGUF file)
		// This allows multiple GGUF files in the same directory to be detected
		model := &ModelInfo{
			ID:         fmt.Sprintf("%x", sha256.Sum256([]byte(weightPath))),
			Name:       strings.TrimSuffix(filepath.Base(weightPath), ".gguf"),
			Type:       p.typeHint,
			Path:       weightPath, // Use file path for uniqueness
			Format:     p.format,
			SizeBytes:  info.Size(),
			ModelClass: "unknown",
		}

		// Parse GGUF header metadata for arch, params, class
		if meta := parseGGUFMeta(weightPath); meta != nil {
			modelType := jsonStr(meta, "model_type", "")
			model.DetectedArch = detectArch(modelType)
			if model.Type == "" {
				model.Type = detectModelType(model.DetectedArch)
			}

			hiddenSize := jsonInt(meta, "hidden_size")
			numLayers := jsonInt(meta, "num_hidden_layers")
			model.DetectedParams = estimateParams(hiddenSize, numLayers)
			model.ModelClass = detectModelClass(meta)

			if model.ModelClass == "moe" {
				baseParams := calculateDenseParams(hiddenSize, numLayers)
				model.TotalParams, model.ActiveParams = calculateMOEParams(meta, meta, baseParams)
			} else if model.ModelClass == "dense" {
				model.TotalParams = calculateDenseParamsFromConfig(meta, meta)
				model.ActiveParams = model.TotalParams
			}
		}

		// Detect quantization from filename
		weightName := filepath.Base(weightPath)
		model.Quantization, model.QuantSrc = detectQuantization(nil, weightName, p.format)

		if model.Type == "" {
			model.Type = "llm" // Default GGUF models to LLM
		}

		models = append(models, model)
	}
	return models
}

// --- GGUF header parser ---

// parseGGUFMeta reads GGUF file header metadata and returns a config.json-compatible map.
// Only reads scalar/string metadata; skips arrays (tokenizer data).
func parseGGUFMeta(path string) map[string]any {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()

	var header struct {
		Magic       uint32
		Version     uint32
		TensorCount uint64
		KVCount     uint64
	}
	if err := binary.Read(f, binary.LittleEndian, &header); err != nil {
		return nil
	}
	if header.Magic != 0x46554747 || header.Version < 2 || header.Version > 3 {
		return nil
	}
	if header.KVCount > 10000 {
		return nil
	}

	// Read all scalar/string KV pairs; for arrays, record element count
	raw := make(map[string]any)
	for i := uint64(0); i < header.KVCount; i++ {
		key, err := ggufReadString(f)
		if err != nil {
			break
		}
		var vtype uint32
		if err := binary.Read(f, binary.LittleEndian, &vtype); err != nil {
			break
		}
		if vtype == 9 { // ARRAY — skip data but record count
			count, err := ggufSkipArray(f)
			if err != nil {
				break
			}
			raw[key+".count"] = count
		} else {
			val, err := ggufReadValue(f, vtype)
			if err != nil {
				break
			}
			raw[key] = val
		}
	}

	// Convert to config.json-compatible map
	arch, _ := raw["general.architecture"].(string)
	config := map[string]any{"model_type": arch}

	keyMap := map[string]string{
		".block_count":                       "num_hidden_layers",
		".embedding_length":                  "hidden_size",
		".feed_forward_length":               "intermediate_size",
		".attention.head_count":              "num_attention_heads",
		".attention.head_count_kv":           "num_key_value_heads",
		".vocab_size":                        "vocab_size",
		".expert_count":                      "num_experts",
		".expert_used_count":                 "num_experts_per_tok",
		".expert_feed_forward_length":        "moe_intermediate_size",
		".expert_shared_feed_forward_length": "shared_expert_intermediate_size",
	}
	for suffix, configKey := range keyMap {
		if v, ok := raw[arch+suffix]; ok {
			config[configKey] = v
		}
	}

	// Vocab size: prefer header field, fall back to tokenizer array length
	if jsonInt(config, "vocab_size") == 0 {
		if count, ok := raw["tokenizer.ggml.tokens.count"]; ok {
			config["vocab_size"] = count
		}
	}

	return config
}

func ggufReadString(r io.Reader) (string, error) {
	var length uint64
	if err := binary.Read(r, binary.LittleEndian, &length); err != nil {
		return "", err
	}
	if length > 1<<20 { // 1MB safety limit
		return "", fmt.Errorf("gguf string too long: %d", length)
	}
	buf := make([]byte, length)
	if _, err := io.ReadFull(r, buf); err != nil {
		return "", err
	}
	return string(buf), nil
}

func ggufReadValue(r io.ReadSeeker, vtype uint32) (any, error) {
	switch vtype {
	case 0: // UINT8
		var v uint8
		return v, binary.Read(r, binary.LittleEndian, &v)
	case 1: // INT8
		var v int8
		return v, binary.Read(r, binary.LittleEndian, &v)
	case 2: // UINT16
		var v uint16
		return v, binary.Read(r, binary.LittleEndian, &v)
	case 3: // INT16
		var v int16
		return v, binary.Read(r, binary.LittleEndian, &v)
	case 4: // UINT32
		var v uint32
		return v, binary.Read(r, binary.LittleEndian, &v)
	case 5: // INT32
		var v int32
		return v, binary.Read(r, binary.LittleEndian, &v)
	case 6: // FLOAT32
		var v float32
		return v, binary.Read(r, binary.LittleEndian, &v)
	case 7: // BOOL
		var v uint8
		err := binary.Read(r, binary.LittleEndian, &v)
		return v != 0, err
	case 8: // STRING
		return ggufReadString(r)
	case 9: // ARRAY — skip past (not normally reached; arrays handled in parseGGUFMeta)
		_, err := ggufSkipArray(r)
		return nil, err
	case 10: // UINT64
		var v uint64
		return v, binary.Read(r, binary.LittleEndian, &v)
	case 11: // INT64
		var v int64
		return v, binary.Read(r, binary.LittleEndian, &v)
	case 12: // FLOAT64
		var v float64
		return v, binary.Read(r, binary.LittleEndian, &v)
	default:
		return nil, fmt.Errorf("unknown gguf value type %d", vtype)
	}
}

func ggufSkipArray(r io.ReadSeeker) (uint64, error) {
	var elemType uint32
	if err := binary.Read(r, binary.LittleEndian, &elemType); err != nil {
		return 0, err
	}
	var count uint64
	if err := binary.Read(r, binary.LittleEndian, &count); err != nil {
		return 0, err
	}
	// Fixed-size elements: seek past in one shot
	if sz := ggufElemSize(elemType); sz > 0 {
		_, err := r.Seek(int64(count)*int64(sz), io.SeekCurrent)
		return count, err
	}
	// String array: read each string's length and seek past data
	if elemType == 8 {
		for i := uint64(0); i < count; i++ {
			var length uint64
			if err := binary.Read(r, binary.LittleEndian, &length); err != nil {
				return count, err
			}
			if _, err := r.Seek(int64(length), io.SeekCurrent); err != nil {
				return count, err
			}
		}
		return count, nil
	}
	return 0, fmt.Errorf("cannot skip gguf array of type %d", elemType)
}

func ggufElemSize(vtype uint32) int {
	switch vtype {
	case 0, 1, 7:
		return 1 // UINT8, INT8, BOOL
	case 2, 3:
		return 2 // UINT16, INT16
	case 4, 5, 6:
		return 4 // UINT32, INT32, FLOAT32
	case 10, 11, 12:
		return 8 // UINT64, INT64, FLOAT64
	default:
		return 0
	}
}
