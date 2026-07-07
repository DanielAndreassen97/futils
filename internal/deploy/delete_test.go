package deploy

import (
	"testing"

	"github.com/DanielAndreassen97/futils/internal/fabric"
)

// wsIDFake implements only DeleteItem; everything else panics via the
// embedded nil interface — DeleteItems must not call anything else.
type wsIDFake struct{ FabricClient }

func (wsIDFake) DeleteItem(token, workspaceID, itemID string) error { return nil }

func TestDeleteItemsSetsWorkspaceID(t *testing.T) {
	target := fabric.Workspace{ID: "ws-1", DisplayName: "WS"}
	res := DeleteItems(wsIDFake{}, "tok", target, []DeleteTarget{
		{Name: "NB_A", Type: "Notebook", ID: "11111111-1111-1111-1111-111111111111"},
	})
	if len(res) != 1 {
		t.Fatalf("got %d results, want 1", len(res))
	}
	if res[0].WorkspaceID != "ws-1" {
		t.Fatalf("WorkspaceID = %q, want %q", res[0].WorkspaceID, "ws-1")
	}
}
