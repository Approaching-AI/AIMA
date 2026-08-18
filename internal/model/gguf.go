package model

import (
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// ggufShardRe matches llama.cpp split-GGUF filenames, e.g.
// "Qwen3.5-122B-A10B-Q4_K_M-00001-of-00002.gguf" → (base, index, total).
var ggufShardRe = regexp.MustCompile(`(?i)^(.+)-(\d+)-of-(\d+)\.gguf$`)

// ggufGroup is one logical GGUF model: either a single file, or a set of split
// shards that llama.cpp loads by opening the first shard.
type ggufGroup struct {
	name    string   // model name: filename without .gguf and shard suffix
	primary string   // path to load (first shard, or the only file)
	parts   []string // all files belonging to this model
}

// isMMProjFile reports whether a GGUF base filename is a multimodal projector
// (mmproj). A projector is an attachment to a vision model, not a standalone
// model, so it must not be surfaced as a deployable model. The scanner already
// skips "mmproj" subdirectories (scanner.yaml); this covers the file-name form.
func isMMProjFile(baseNoExt string) bool {
	return strings.Contains(strings.ToLower(baseNoExt), "mmproj")
}

// groupGGUFModels collapses split shards into one logical model and drops mmproj
// projectors. A non-split .gguf stays a single-file model. Output is
// deterministic for sorted input: grouped shards (first-seen order) then
// singles (input order).
func groupGGUFModels(files []string) []ggufGroup {
	type acc struct {
		group  *ggufGroup
		minIdx int
	}
	groups := map[string]*acc{}
	var order []string
	var singles []ggufGroup

	for _, f := range files {
		base := filepath.Base(f)
		nameNoExt := base[:len(base)-len(filepath.Ext(base))]
		if isMMProjFile(nameNoExt) {
			continue // projector, not a standalone model
		}
		if m := ggufShardRe.FindStringSubmatch(base); m != nil {
			groupName := m[1]
			idx, _ := strconv.Atoi(m[2])
			key := filepath.Dir(f) + string(filepath.Separator) + groupName
			a, ok := groups[key]
			if !ok {
				a = &acc{group: &ggufGroup{name: groupName, primary: f}, minIdx: idx}
				groups[key] = a
				order = append(order, key)
			}
			a.group.parts = append(a.group.parts, f)
			if idx < a.minIdx { // primary = lowest-numbered shard
				a.minIdx = idx
				a.group.primary = f
			}
		} else {
			singles = append(singles, ggufGroup{name: nameNoExt, primary: f, parts: []string{f}})
		}
	}

	out := make([]ggufGroup, 0, len(order)+len(singles))
	for _, key := range order {
		g := groups[key].group
		sort.Strings(g.parts)
		out = append(out, *g)
	}
	return append(out, singles...)
}

// detectGGUFModels detects GGUF models in a directory. Split shards
// ("...-00001-of-00003.gguf") collapse into one model whose Path is the first
// shard (llama.cpp auto-loads the rest) and whose size is the sum of all parts;
// mmproj projector files are excluded.
func detectGGUFModels(dir string, entries []os.DirEntry, p ModelPattern, minSize int64) []*ModelInfo {
	weightFiles := findAllWeightFiles(dir, entries, p.weightExts)
	if len(weightFiles) == 0 {
		return nil
	}

	var models []*ModelInfo
	for _, g := range groupGGUFModels(weightFiles) {
		// Size = sum of all shards; compare the whole model against the minimum.
		var totalSize int64
		for _, part := range g.parts {
			if info, err := os.Stat(part); err == nil {
				totalSize += info.Size()
			}
		}
		if totalSize < minSize {
			continue
		}

		model := &ModelInfo{
			ID:         fmt.Sprintf("%x", sha256.Sum256([]byte(g.primary))),
			Name:       g.name,
			Type:       p.typeHint,
			Path:       g.primary, // first shard — llama.cpp loads the rest automatically
			Format:     p.format,
			SizeBytes:  totalSize,
			ModelClass: "unknown",
		}

		// Parse GGUF header metadata for arch, params, class (from the first shard)
		if meta := parseGGUFMeta(g.primary); meta != nil {
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

		// Detect quantization from the primary file name
		model.Quantization, model.QuantSrc = detectQuantization(nil, filepath.Base(g.primary), p.format)

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
		".context_length":                    "max_position_embeddings",
		".embedding_length":                  "hidden_size",
		".feed_forward_length":               "intermediate_size",
		".attention.head_count":              "num_attention_heads",
		".attention.head_count_kv":           "num_key_value_heads",
		".attention.key_length":              "head_dim",
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

// KVArch holds the GGUF architecture fields needed to size the KV cache.
type KVArch struct {
	NLayer    int // transformer blocks (block_count)
	NHeadKV   int // KV heads (head_count_kv; equals head_count for MHA)
	HeadDim   int // per-head dimension
	NCtxTrain int // trained context length (max_position_embeddings); 0 if unknown
}

// KVBytesPerToken returns the f16 KV-cache size for one token across all layers:
// 2 (K+V) * n_layer * n_head_kv * head_dim * 2 bytes.
func (a KVArch) KVBytesPerToken() int64 {
	return int64(2) * int64(a.NLayer) * int64(a.NHeadKV) * int64(a.HeadDim) * 2
}

// ReadKVArch parses a GGUF file's header and returns the architecture needed to
// estimate KV-cache memory. ok is false when the file can't be parsed or lacks
// the required fields (caller should then skip memory-based context sizing).
func ReadKVArch(path string) (KVArch, bool) {
	meta := parseGGUFMeta(path)
	if meta == nil {
		return KVArch{}, false
	}
	nLayer := jsonInt(meta, "num_hidden_layers")
	nHeadKV := jsonInt(meta, "num_key_value_heads")
	nHead := jsonInt(meta, "num_attention_heads")
	if nHeadKV == 0 {
		nHeadKV = nHead // MHA: KV heads == attention heads
	}
	headDim := jsonInt(meta, "head_dim")
	if headDim == 0 && nHead > 0 {
		headDim = jsonInt(meta, "hidden_size") / nHead
	}
	if nLayer == 0 || nHeadKV == 0 || headDim == 0 {
		return KVArch{}, false
	}
	return KVArch{
		NLayer:    nLayer,
		NHeadKV:   nHeadKV,
		HeadDim:   headDim,
		NCtxTrain: jsonInt(meta, "max_position_embeddings"),
	}, true
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
