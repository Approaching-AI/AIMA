package catalog

import "embed"

//go:embed hardware engines models partitions stack scanner.yaml
var FS embed.FS
