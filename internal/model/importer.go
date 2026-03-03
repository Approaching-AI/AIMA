package model

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// Import registers a model from a local path. If srcPath is already under destDir,
// no copy is performed. Otherwise, files are copied to destDir.
func Import(ctx context.Context, srcPath, destDir string) (*ModelInfo, error) {
	srcAbs, err := filepath.Abs(srcPath)
	if err != nil {
		return nil, fmt.Errorf("resolve source path %s: %w", srcPath, err)
	}
	destAbs, err := filepath.Abs(destDir)
	if err != nil {
		return nil, fmt.Errorf("resolve dest path %s: %w", destDir, err)
	}

	// Validate source exists
	srcInfo, err := os.Stat(srcAbs)
	if err != nil {
		return nil, fmt.Errorf("import model from %s: %w", srcPath, err)
	}
	if !srcInfo.IsDir() {
		return nil, fmt.Errorf("import model from %s: not a directory", srcPath)
	}

	// Validate it looks like a model
	if !isModelDirectory(srcAbs) {
		return nil, fmt.Errorf("import model from %s: no model files found (need config.json+safetensors or .gguf)", srcPath)
	}

	modelDir := srcAbs

	// If source is not already under dest, copy it
	if !isSubpath(srcAbs, destAbs) {
		modelDir = filepath.Join(destAbs, filepath.Base(srcAbs))
		if err := copyDir(ctx, srcAbs, modelDir); err != nil {
			return nil, fmt.Errorf("import model from %s: %w", srcPath, err)
		}
	}

	// Scan the (possibly copied) model directory; no size floor since we know it's a model
	models, err := Scan(ctx, ScanOptions{Paths: []string{filepath.Dir(modelDir)}, MinModelSizeBytes: 1})
	if err != nil {
		return nil, fmt.Errorf("scan imported model: %w", err)
	}

	prefix := modelDir + string(filepath.Separator)
	for _, m := range models {
		// Match directory path (safetensors/pytorch) or file within it (GGUF)
		if m.Path == modelDir || strings.HasPrefix(m.Path, prefix) {
			return m, nil
		}
	}

	return nil, fmt.Errorf("import model from %s: scan did not detect model after import", srcPath)
}

func isModelDirectory(dir string) bool {
	// Check for HuggingFace format
	if hasFile(dir, "config.json") && hasFileWithExtension(dir, ".safetensors") {
		return true
	}
	// Check for GGUF format
	if hasFileWithExtension(dir, ".gguf") {
		return true
	}
	return false
}

func isSubpath(child, parent string) bool {
	rel, err := filepath.Rel(parent, child)
	if err != nil {
		return false
	}
	return !strings.HasPrefix(rel, "..")
}

func hasFile(dir, name string) bool {
	_, err := os.Stat(filepath.Join(dir, name))
	return err == nil
}

func hasFileWithExtension(dir, ext string) bool {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false
	}
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(strings.ToLower(e.Name()), ext) {
			return true
		}
	}
	return false
}

func copyDir(ctx context.Context, src, dst string) error {
	if err := os.MkdirAll(dst, 0o755); err != nil {
		return fmt.Errorf("create directory %s: %w", dst, err)
	}

	entries, err := os.ReadDir(src)
	if err != nil {
		return fmt.Errorf("read directory %s: %w", src, err)
	}

	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return err
		}

		srcPath := filepath.Join(src, entry.Name())
		dstPath := filepath.Join(dst, entry.Name())

		if entry.IsDir() {
			if err := copyDir(ctx, srcPath, dstPath); err != nil {
				return err
			}
			continue
		}

		if err := copyFile(srcPath, dstPath); err != nil {
			return err
		}
	}

	return nil
}

func copyFile(src, dst string) error {
	sf, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("open %s: %w", src, err)
	}
	defer sf.Close()

	df, err := os.Create(dst)
	if err != nil {
		return fmt.Errorf("create %s: %w", dst, err)
	}

	if _, err := io.Copy(df, sf); err != nil {
		df.Close()
		return fmt.Errorf("copy %s to %s: %w", src, dst, err)
	}

	return df.Close()
}
