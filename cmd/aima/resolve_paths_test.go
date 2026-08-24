package main

import (
	"testing"

	"github.com/jguan/aima/internal/knowledge"
)

func TestExpandDataDirInVolumesDoesNotMutateCatalogInput(t *testing.T) {
	input := []knowledge.ContainerVolume{
		{
			Name:      "runtime",
			HostPath:  "{{.DataDir}}/runtime/model",
			MountPath: "/runtime",
		},
	}

	first := expandDataDirInVolumes(input, "/data/first")
	second := expandDataDirInVolumes(input, "/data/second")

	if got := first[0].HostPath; got != "/data/first/runtime/model" {
		t.Fatalf("first HostPath = %q", got)
	}
	if got := second[0].HostPath; got != "/data/second/runtime/model" {
		t.Fatalf("second HostPath = %q", got)
	}
	if got := input[0].HostPath; got != "{{.DataDir}}/runtime/model" {
		t.Fatalf("catalog input mutated: HostPath = %q", got)
	}
}
