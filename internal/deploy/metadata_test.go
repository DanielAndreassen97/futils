package deploy

import "testing"

// pyNotebook is the Fabric .py source format: a trailing "# METADATA" section
// with "# META"-prefixed JSON, exactly as committed to git.
const pyNotebook = `# Fabric notebook source

# CELL ********************

print("hi")

# METADATA ********************

# META {
# META   "dependencies": {
# META     "lakehouse": {
# META       "default_lakehouse": "11111111-1111-1111-1111-111111111111",
# META       "default_lakehouse_name": "LH_ConfigLog",
# META       "default_lakehouse_workspace_id": "22222222-2222-2222-2222-222222222222",
# META       "known_lakehouses": [
# META         { "id": "33333333-3333-3333-3333-333333333333" }
# META       ]
# META     }
# META   }
# META }
`

const ipynbNotebook = `{"cells":[],"metadata":{"dependencies":{"lakehouse":{"default_lakehouse":"aaaa1111-1111-1111-1111-111111111111","default_lakehouse_name":"LH_Gold","default_lakehouse_workspace_id":"bbbb2222-2222-2222-2222-222222222222"}}},"nbformat":4}`

func TestParseNotebookLakehousePy(t *testing.T) {
	lh, ok := parseNotebookLakehouse([]byte(pyNotebook))
	if !ok {
		t.Fatal("expected a lakehouse block")
	}
	if lh.DefaultLakehouse != "11111111-1111-1111-1111-111111111111" {
		t.Errorf("DefaultLakehouse = %q", lh.DefaultLakehouse)
	}
	if lh.DefaultLakehouseName != "LH_ConfigLog" {
		t.Errorf("DefaultLakehouseName = %q", lh.DefaultLakehouseName)
	}
	if lh.DefaultLakehouseWorkspaceID != "22222222-2222-2222-2222-222222222222" {
		t.Errorf("DefaultLakehouseWorkspaceID = %q", lh.DefaultLakehouseWorkspaceID)
	}
	if len(lh.KnownLakehouses) != 1 || lh.KnownLakehouses[0].ID != "33333333-3333-3333-3333-333333333333" {
		t.Errorf("KnownLakehouses = %#v", lh.KnownLakehouses)
	}
}

func TestParseNotebookLakehouseIpynb(t *testing.T) {
	lh, ok := parseNotebookLakehouse([]byte(ipynbNotebook))
	if !ok {
		t.Fatal("expected a lakehouse block")
	}
	if lh.DefaultLakehouseName != "LH_Gold" {
		t.Errorf("DefaultLakehouseName = %q", lh.DefaultLakehouseName)
	}
}

func TestParseNotebookLakehouseAbsent(t *testing.T) {
	noLakehouse := "# Fabric notebook source\n\n# CELL ********************\n\nprint(1)\n"
	if _, ok := parseNotebookLakehouse([]byte(noLakehouse)); ok {
		t.Error("expected no lakehouse block for a notebook without one")
	}
	if _, ok := parseNotebookLakehouse([]byte("not a notebook at all")); ok {
		t.Error("expected no lakehouse block for arbitrary content")
	}
}
