package ci_test

import (
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

const buildCommitExpression = "${{ github.event.pull_request.head.sha || github.sha }}"

type workflowDocument struct {
	Name        string                   `yaml:"name"`
	On          map[string]triggerConfig `yaml:"on"`
	Permissions map[string]string        `yaml:"permissions"`
	Jobs        map[string]jobConfig     `yaml:"jobs"`
}

type triggerConfig struct {
	Branches []string `yaml:"branches"`
}

type jobConfig struct {
	Env   map[string]string `yaml:"env"`
	Steps []stepConfig      `yaml:"steps"`
}

type stepConfig struct {
	Name string         `yaml:"name"`
	Uses string         `yaml:"uses"`
	Run  string         `yaml:"run"`
	With map[string]any `yaml:"with"`
}

func TestAMD395WindowsWorkflowContract(t *testing.T) {
	workflow := loadWorkflow(t, "amd395-windows-build.yml")

	if workflow.Name != "AMD395 Windows Build" {
		t.Fatalf("workflow name = %q", workflow.Name)
	}

	wantTriggers := map[string]triggerConfig{
		"pull_request": {Branches: []string{"amd395-win"}},
		"push":         {Branches: []string{"amd395-win"}},
	}
	if !reflect.DeepEqual(workflow.On, wantTriggers) {
		t.Fatalf("workflow triggers = %#v, want %#v", workflow.On, wantTriggers)
	}

	if !reflect.DeepEqual(workflow.Permissions, map[string]string{"contents": "read"}) {
		t.Fatalf("workflow permissions = %#v", workflow.Permissions)
	}

	job, ok := workflow.Jobs["test-and-package"]
	if !ok {
		t.Fatal("test-and-package job is missing")
	}
	if job.Env["GIT_COMMIT"] != buildCommitExpression {
		t.Fatalf("job GIT_COMMIT = %q", job.Env["GIT_COMMIT"])
	}

	checkout := findStep(t, job.Steps, "Check out source commit")
	if checkout.Uses != "actions/checkout@v6" {
		t.Fatalf("checkout action = %q", checkout.Uses)
	}
	if checkout.With["ref"] != buildCommitExpression {
		t.Fatalf("checkout ref = %#v", checkout.With["ref"])
	}

	unitTests := findStep(t, job.Steps, "Unit tests")
	if strings.TrimSpace(unitTests.Run) != "go test ./..." {
		t.Fatalf("unit test command = %q", unitTests.Run)
	}

	packageStep := findStep(t, job.Steps, "Build Windows package")
	if !strings.Contains(packageStep.Run, "bash ./scripts/package-amd395-windows.sh") {
		t.Fatalf("package step does not call the repository script: %q", packageStep.Run)
	}

	upload := findStep(t, job.Steps, "Upload Windows package")
	if upload.Uses != "actions/upload-artifact@v7" {
		t.Fatalf("upload action = %q", upload.Uses)
	}
	if upload.With["if-no-files-found"] != "error" {
		t.Fatalf("if-no-files-found = %#v", upload.With["if-no-files-found"])
	}
	if upload.With["retention-days"] != 30 {
		t.Fatalf("retention-days = %#v", upload.With["retention-days"])
	}
}

func TestAMD395LinuxWorkflowContract(t *testing.T) {
	workflow := loadWorkflow(t, "amd395-linux-build.yml")

	if workflow.Name != "AMD395 Linux Build" {
		t.Fatalf("workflow name = %q", workflow.Name)
	}
	wantTriggers := map[string]triggerConfig{
		"pull_request": {Branches: []string{"amd395-linux"}},
		"push":         {Branches: []string{"amd395-linux"}},
	}
	if !reflect.DeepEqual(workflow.On, wantTriggers) {
		t.Fatalf("workflow triggers = %#v, want %#v", workflow.On, wantTriggers)
	}
	if !reflect.DeepEqual(workflow.Permissions, map[string]string{"contents": "read"}) {
		t.Fatalf("workflow permissions = %#v", workflow.Permissions)
	}

	job, ok := workflow.Jobs["test-and-package"]
	if !ok {
		t.Fatal("test-and-package job is missing")
	}
	if job.Env["GIT_COMMIT"] != buildCommitExpression {
		t.Fatalf("job GIT_COMMIT = %q", job.Env["GIT_COMMIT"])
	}
	checkout := findStep(t, job.Steps, "Check out source commit")
	if checkout.Uses != "actions/checkout@v6" || checkout.With["ref"] != buildCommitExpression {
		t.Fatalf("checkout step = %#v", checkout)
	}
	packageStep := findStep(t, job.Steps, "Build Linux package")
	if !strings.Contains(packageStep.Run, "bash ./scripts/package-amd395-linux.sh") {
		t.Fatalf("package step does not call the repository script: %q", packageStep.Run)
	}
	upload := findStep(t, job.Steps, "Upload Linux package")
	if upload.Uses != "actions/upload-artifact@v7" || upload.With["if-no-files-found"] != "error" || upload.With["retention-days"] != 30 {
		t.Fatalf("upload step = %#v", upload)
	}
}

func loadWorkflow(t *testing.T, workflowName string) workflowDocument {
	t.Helper()
	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve test filename")
	}
	path := filepath.Join(filepath.Dir(filename), "..", "..", ".github", "workflows", workflowName)
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read workflow: %v", err)
	}

	var workflow workflowDocument
	if err := yaml.Unmarshal(raw, &workflow); err != nil {
		t.Fatalf("parse workflow YAML: %v", err)
	}
	return workflow
}

func findStep(t *testing.T, steps []stepConfig, name string) stepConfig {
	t.Helper()
	for _, step := range steps {
		if step.Name == name {
			return step
		}
	}
	t.Fatalf("workflow step %q is missing", name)
	return stepConfig{}
}
