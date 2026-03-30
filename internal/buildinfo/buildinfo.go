package buildinfo

import (
	_ "embed"
	"strings"
)

//go:embed series.txt
var devSeries string

func defaultVersion() string {
	series := strings.TrimSpace(devSeries)
	if series == "" {
		return "dev"
	}
	return series + "-dev"
}

// Version information is injected at build time via -ldflags.
var (
	Version   = defaultVersion()
	BuildTime = "unknown"
	GitCommit = "none"
)
