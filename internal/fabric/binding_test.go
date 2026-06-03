package fabric

import (
	"encoding/json"
	"strings"
	"testing"
)

// brokenBindingIpynb is the exact shape Fabric serves for a notebook that
// pins a lakehouse GUID but lost its name + workspace id — the bug this
// feature repairs.
const brokenBindingIpynb = `{
  "metadata": {
    "dependencies": {
      "lakehouse": {
        "default_lakehouse": "18fbaeb6-b04f-429b-880a-5c97deb912bd",
        "default_lakehouse_name": "",
        "default_lakehouse_workspace_id": ""
      }
    }
  },
  "cells": []
}`

const completeBindingIpynb = `{
  "metadata": {
    "dependencies": {
      "lakehouse": {
        "default_lakehouse": "18fbaeb6-b04f-429b-880a-5c97deb912bd",
        "default_lakehouse_name": "LH_ConfigLog",
        "default_lakehouse_workspace_id": "8ce92b5f-826e-4602-b7f4-43dfa9098ec0"
      }
    }
  },
  "cells": []
}`

func TestParseLakehouseBinding_Broken(t *testing.T) {
	b, err := ParseLakehouseBinding([]byte(brokenBindingIpynb))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if b.LakehouseID != "18fbaeb6-b04f-429b-880a-5c97deb912bd" {
		t.Errorf("LakehouseID = %q", b.LakehouseID)
	}
	if b.WorkspaceID != "" {
		t.Errorf("WorkspaceID = %q, want empty", b.WorkspaceID)
	}
	if !b.NeedsWorkspaceResolution() {
		t.Error("NeedsWorkspaceResolution() = false, want true for a pinned lakehouse with no workspace")
	}
}

func TestParseLakehouseBinding_Complete(t *testing.T) {
	b, err := ParseLakehouseBinding([]byte(completeBindingIpynb))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if b.Name != "LH_ConfigLog" || b.WorkspaceID == "" {
		t.Errorf("expected complete binding, got %#v", b)
	}
	if b.NeedsWorkspaceResolution() {
		t.Error("NeedsWorkspaceResolution() = true, want false for a complete binding")
	}
}

func TestParseLakehouseBinding_NoLakehouse(t *testing.T) {
	// A notebook with no lakehouse dependency at all — futils must treat this
	// as "run with no default lakehouse", never inventing one.
	nb := `{"metadata": {"kernelspec": {"name": "synapse_pyspark"}}, "cells": []}`
	b, err := ParseLakehouseBinding([]byte(nb))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if b.LakehouseID != "" {
		t.Errorf("LakehouseID = %q, want empty", b.LakehouseID)
	}
	if b.NeedsWorkspaceResolution() {
		t.Error("NeedsWorkspaceResolution() = true, want false when no lakehouse is pinned")
	}
}

func TestParseLakehouseBinding_MalformedJSON(t *testing.T) {
	if _, err := ParseLakehouseBinding([]byte(`{not json`)); err == nil {
		t.Error("expected error on malformed JSON, got nil")
	}
}

func TestBuildRunBody_WithLakehouseOverride(t *testing.T) {
	lh := &DefaultLakehouse{Name: "LH_ConfigLog", ID: "18fbaeb6", WorkspaceID: "8ce92b5f"}
	raw, err := buildRunBody([]JobInput{{Name: "rewrite_table", Value: true, Type: TypeBool}}, lh)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var got struct {
		ExecutionData struct {
			Parameters    map[string]struct{ Value any } `json:"parameters"`
			Configuration struct {
				DefaultLakehouse *DefaultLakehouse `json:"defaultLakehouse"`
			} `json:"configuration"`
		} `json:"executionData"`
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("payload is not valid JSON: %v\n%s", err, raw)
	}

	dl := got.ExecutionData.Configuration.DefaultLakehouse
	if dl == nil {
		t.Fatalf("defaultLakehouse missing from payload: %s", raw)
	}
	if dl.ID != "18fbaeb6" || dl.WorkspaceID != "8ce92b5f" || dl.Name != "LH_ConfigLog" {
		t.Errorf("defaultLakehouse = %#v", dl)
	}
	if _, ok := got.ExecutionData.Parameters["rewrite_table"]; !ok {
		t.Errorf("parameters not preserved alongside defaultLakehouse: %s", raw)
	}
}

func TestBuildRunBody_NoLakehouse_OmitsConfiguration(t *testing.T) {
	// The common case: no override. The configuration block must be absent
	// entirely (omitempty), so a correctly-bound notebook sees the exact same
	// payload as before this feature existed.
	raw, err := buildRunBody(nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if strings.Contains(string(raw), "configuration") {
		t.Errorf("expected no configuration block when lakehouse is nil, got: %s", raw)
	}
	if strings.Contains(string(raw), "defaultLakehouse") {
		t.Errorf("expected no defaultLakehouse when nil, got: %s", raw)
	}
}
