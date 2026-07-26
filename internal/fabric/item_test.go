package fabric

import (
	"net/http"
	"strings"
	"testing"
)

const testItemID = "44444444-4444-4444-4444-444444444444"

func TestRenameItemPatchesDisplayNameOnly(t *testing.T) {
	// UpdateItem sends displayName AND description together, which is right for
	// deploy — it knows both values from git. Here the description would be a
	// stale local copy, so a rename must not carry one.
	transport := stubTransport(t, seqResponse{status: 200, body: `{
	  "id": "` + testItemID + `",
	  "displayName": "nb_ingest_customers",
	  "type": "Notebook",
	  "description": "Loads customers"
	}`})

	item, err := RenameItem("tok", testWSID, testItemID, "nb_ingest_customers")
	if err != nil {
		t.Fatalf("RenameItem: %v", err)
	}
	if item.DisplayName != "nb_ingest_customers" || item.Type != "Notebook" {
		t.Errorf("decoded item = %+v", item)
	}
	if transport.methods[0] != http.MethodPatch {
		t.Errorf("method = %q, want PATCH", transport.methods[0])
	}
	want := baseURL + "/v1/workspaces/" + testWSID + "/items/" + testItemID
	if transport.urls[0] != want {
		t.Errorf("url = %q, want %q", transport.urls[0], want)
	}
	body := decodeBody(t, transport.bodies[0])
	if body["displayName"] != "nb_ingest_customers" {
		t.Errorf("displayName = %v", body["displayName"])
	}
	if _, ok := body["description"]; ok {
		t.Error("a rename must not send description")
	}
}

func TestSetItemDescriptionSendsEmptyStringToClear(t *testing.T) {
	// An empty description clears the field, so it is a real value and has to
	// be sent rather than omitted.
	transport := stubTransport(t, seqResponse{status: 200, body: `{"id":"` + testItemID + `","displayName":"nb_ingest"}`})

	if _, err := SetItemDescription("tok", testWSID, testItemID, ""); err != nil {
		t.Fatalf("SetItemDescription: %v", err)
	}
	body := decodeBody(t, transport.bodies[0])
	desc, ok := body["description"]
	if !ok {
		t.Fatal("description must always be sent, including when empty")
	}
	if desc != "" {
		t.Errorf("description = %v, want an empty string", desc)
	}
	if _, ok := body["displayName"]; ok {
		t.Error("setting the description must not send displayName")
	}
}

func TestPatchItemForbiddenMentionsWriteAccess(t *testing.T) {
	// Fabric's 403 body says only "InsufficientPrivileges"; the Update Item API
	// needs read AND write on the item, and saying so is what the user can act on.
	stubTransport(t, seqResponse{status: 403, body: `{"errorCode":"InsufficientPrivileges"}`})

	_, err := RenameItem("tok", testWSID, testItemID, "whatever")
	if err == nil {
		t.Fatal("expected an error on 403")
	}
	if !strings.Contains(strings.ToLower(err.Error()), "write") {
		t.Errorf("403 must mention write permission, got %v", err)
	}
}

func TestPatchItemReportsOtherAPIErrors(t *testing.T) {
	stubTransport(t, seqResponse{status: 400, body: `{"errorCode":"InvalidRequest"}`})

	if _, err := RenameItem("tok", testWSID, testItemID, "bad name"); err == nil {
		t.Fatal("expected an error on 400")
	} else if !strings.Contains(err.Error(), "InvalidRequest") {
		t.Errorf("error must surface Fabric's own message, got %v", err)
	}
}

func TestItemPatchRejectsInvalidIDs(t *testing.T) {
	if _, err := RenameItem("tok", "not-a-uuid", testItemID, "x"); err == nil {
		t.Error("expected a validation error for a non-UUID workspace id")
	}
	if _, err := RenameItem("tok", testWSID, "not-a-uuid", "x"); err == nil {
		t.Error("expected a validation error for a non-UUID item id")
	}
	if _, err := SetItemDescription("tok", testWSID, "not-a-uuid", "x"); err == nil {
		t.Error("expected a validation error for a non-UUID item id")
	}
}

func TestMaxItemDescLenIsNotTheWorkspaceLimit(t *testing.T) {
	// Items cap descriptions at 256 characters; workspaces allow 4000. Reusing
	// the workspace constant would let a 4000-character description through to
	// a guaranteed rejection.
	if MaxItemDescLen != 256 {
		t.Errorf("MaxItemDescLen = %d, want 256", MaxItemDescLen)
	}
	if MaxItemDescLen == MaxWorkspaceDescLen {
		t.Error("item and workspace description limits must not share a value")
	}
}
