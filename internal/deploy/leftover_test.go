package deploy

import (
	"strings"
	"testing"
)

func TestScanLeftoversWorkspaceGUID(t *testing.T) {
	rb := pipelineRebinderGUIDs()
	refs := rb.ScanLeftovers("notebook-content.py", []byte(`path = "abfss://`+gWSDev+`@onelake"`))
	if len(refs) != 1 || refs[0].Reason != ReasonLeftover || refs[0].GUID != gWSDev {
		t.Fatalf("refs = %#v", refs)
	}
	if !strings.Contains(refs[0].Hint, "DW - DEV - Data") || !strings.Contains(refs[0].Hint, gWSTst) {
		t.Errorf("hint must name the workspace and the target value: %q", refs[0].Hint)
	}
}

func TestScanLeftoversItemGUID(t *testing.T) {
	rb := pipelineRebinderGUIDs()
	refs := rb.ScanLeftovers("notebook-content.py", []byte(gLHDev))
	if len(refs) != 1 || !strings.Contains(refs[0].Hint, "LH_Bronze") {
		t.Fatalf("refs = %#v", refs)
	}
}

func TestScanLeftoversIgnoresUnknownAndIdentity(t *testing.T) {
	rb := pipelineRebinderGUIDs()
	// unknown GUID: not baseline's -> silent
	if refs := rb.ScanLeftovers("p", []byte(gUnkwn)); len(refs) != 0 {
		t.Fatalf("unknown GUID must not warn: %#v", refs)
	}
	// shared reference workspace (same GUID both envs): identity -> silent
	rb.wsMap[gShared] = gShared
	rb.baselineWSNames[gShared] = "Shared reference workspace"
	if refs := rb.ScanLeftovers("p", []byte(gShared)); len(refs) != 0 {
		t.Fatalf("identity mapping must not warn: %#v", refs)
	}
}

func TestScanLeftoversNeverMutates(t *testing.T) {
	rb := pipelineRebinderGUIDs()
	in := []byte(gWSDev)
	rb.ScanLeftovers("p", in)
	if string(in) != gWSDev {
		t.Fatal("scan must not mutate content")
	}
}
