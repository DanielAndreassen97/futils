package deploy

import "testing"

func TestReplaceLogicalIds(t *testing.T) {
	idMap := map[string]string{
		"aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa": "deployed-model-guid",
	}
	in := []byte(`{"datasetReference":"aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"}`)
	out := ReplaceLogicalIds(in, idMap)
	if string(out) != `{"datasetReference":"deployed-model-guid"}` {
		t.Errorf("got %q", out)
	}
}

func TestReplaceLogicalIdsSkipsPlaceholder(t *testing.T) {
	idMap := map[string]string{
		"00000000-0000-0000-0000-000000000000": "should-not-be-used",
	}
	in := []byte("00000000-0000-0000-0000-000000000000")
	out := ReplaceLogicalIds(in, idMap)
	if string(out) != "00000000-0000-0000-0000-000000000000" {
		t.Errorf("placeholder must not be replaced; got %q", out)
	}
}
