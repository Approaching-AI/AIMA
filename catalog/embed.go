package catalog

import "embed"

//go:embed hardware engines models partitions stack scenarios scanner.yaml agent-guide.md ui-onboarding.json
var FS embed.FS
