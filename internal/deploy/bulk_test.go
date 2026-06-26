package deploy

import (
	"strings"
	"testing"
)

func TestStripLogicalID(t *testing.T) {
	in := []byte(`{"$schema":"s","metadata":{"type":"Report","displayName":"R","description":"d"},"config":{"version":"2.0","logicalId":"GIT-LID"}}`)
	out := stripLogicalID(in)
	if strings.Contains(string(out), "GIT-LID") {
		t.Errorf("logicalId not stripped: %s", out)
	}
	if !strings.Contains(string(out), `"displayName":"R"`) {
		t.Errorf("displayName lost: %s", out)
	}
	if !strings.Contains(string(out), `"description":"d"`) {
		t.Errorf("description lost: %s", out)
	}
	if !strings.Contains(string(out), `"version":"2.0"`) {
		t.Errorf("config.version lost: %s", out)
	}
}

func TestStripLogicalIDNoConfigIsNoop(t *testing.T) {
	in := []byte(`{"metadata":{"type":"Report","displayName":"R"}}`)
	out := stripLogicalID(in)
	if string(out) != string(in) {
		t.Errorf("expected unchanged, got %s", out)
	}
}

func TestStripLogicalIDInvalidJSONIsNoop(t *testing.T) {
	in := []byte(`not json`)
	if string(stripLogicalID(in)) != string(in) {
		t.Error("invalid JSON should be returned unchanged")
	}
}
