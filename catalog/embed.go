package catalog

import "embed"

//go:embed hardware engines models partitions stack scenarios benchmarks scanner.yaml agent-guide.md ui-onboarding.json
var FS embed.FS
