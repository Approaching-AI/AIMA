package catalog

import "embed"

//go:embed hardware engines models partitions stack scanner.yaml agent-guide.md
var FS embed.FS
