package mcp

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestExternalToolsCallScanAndList(t *testing.T) {
	s := NewServer()
	var scanCalled bool
	var listCalled bool
	registerExternalTools(s, &ToolDeps{
		ScanExternalServices: func(ctx context.Context) (json.RawMessage, error) {
			scanCalled = true
			return json.RawMessage(`[{"base_url":"http://127.0.0.1:8009"}]`), nil
		},
		ListExternalServices: func(ctx context.Context) (json.RawMessage, error) {
			listCalled = true
			return json.RawMessage(`[{"base_url":"http://127.0.0.1:8009"}]`), nil
		},
		ImportExternalService: func(ctx context.Context, idOrBaseURL string, models []string) (json.RawMessage, error) {
			return json.RawMessage(`{"imported":true,"base_url":"` + idOrBaseURL + `"}`), nil
		},
	})

	scanResult, err := s.ExecuteTool(context.Background(), "external.scan", json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("external.scan: %v", err)
	}
	if !scanCalled || !strings.Contains(toolText(scanResult), "127.0.0.1:8009") {
		t.Fatalf("scanCalled=%v result=%+v", scanCalled, scanResult)
	}

	listResult, err := s.ExecuteTool(context.Background(), "external.list", json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("external.list: %v", err)
	}
	if !listCalled || !strings.Contains(toolText(listResult), "127.0.0.1:8009") {
		t.Fatalf("listCalled=%v result=%+v", listCalled, listResult)
	}

	importResult, err := s.ExecuteTool(context.Background(), "external.import", json.RawMessage(`{"base_url":"http://127.0.0.1:8009"}`))
	if err != nil {
		t.Fatalf("external.import: %v", err)
	}
	if !strings.Contains(toolText(importResult), `"imported":true`) {
		t.Fatalf("import result=%+v", importResult)
	}
}

func toolText(result *ToolResult) string {
	if result == nil || len(result.Content) == 0 {
		return ""
	}
	return result.Content[0].Text
}
