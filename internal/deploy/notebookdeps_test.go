// What: a notebook whose lakehouse binding Fabric leaves out of its default
// (.py) export compares equal to git once the binding is taken from the ipynb
// export, and a binding that really differs still reads as Changed.
//
// Why: Fabric's FabricGitSource export can drop the "dependencies" block that
// the ipynb export of the same item still carries. Without this, every bound
// notebook shows a 13-line diff on every deploy, forever, and a real rebind
// drowns among them.
//
// Example: git binds NB_Load to LH_ConfigLog, the target's .py export has only
// kernel_info, its ipynb export binds LH_ConfigLog: the compare is Unchanged.
package deploy

import (
	"encoding/base64"
	"errors"
	"testing"

	"github.com/DanielAndreassen97/futils/internal/fabric"
)

const (
	lhBound = "aaaaaaaa-0000-0000-0000-000000000001"
	lhOther = "aaaaaaaa-0000-0000-0000-000000000099"
	wsBound = "bbbbbbbb-0000-0000-0000-000000000001"
)

// nb wraps notebook-level metadata lines in a minimal .py notebook.
func nb(meta string) string {
	return "# Fabric notebook source\n\n# METADATA ********************\n\n" + meta +
		"\n\n# CELL ********************\n\nprint(\"Contoso\")\n\n# METADATA ********************\n\n" +
		"# META {\n# META   \"language\": \"python\",\n# META   \"language_group\": \"synapse_pyspark\"\n# META }\n"
}

// nbBound is the notebook as git holds it, bound to LH_ConfigLog.
var nbBound = nb(`# META {
# META   "kernel_info": {
# META     "name": "synapse_pyspark"
# META   },
# META   "dependencies": {
# META     "lakehouse": {
# META       "default_lakehouse": "` + lhBound + `",
# META       "default_lakehouse_name": "LH_ConfigLog",
# META       "default_lakehouse_workspace_id": "` + wsBound + `",
# META       "known_lakehouses": [
# META         {
# META           "id": "` + lhBound + `"
# META         }
# META       ]
# META     },
# META     "environment": {}
# META   }
# META }`)

// nbStripped is the same notebook as Fabric's .py export returns it.
var nbStripped = nb(`# META {
# META   "kernel_info": {
# META     "name": "synapse_pyspark"
# META   }
# META }`)

func ipynbWith(lakehouseID string) []byte {
	return []byte(`{"cells":[],"nbformat":4,"metadata":{"kernel_info":{"name":"synapse_pyspark"},` +
		`"dependencies":{"lakehouse":{"default_lakehouse":"` + lakehouseID + `",` +
		`"default_lakehouse_name":"LH_ConfigLog","default_lakehouse_workspace_id":"` + wsBound + `",` +
		`"known_lakehouses":[{"id":"` + lakehouseID + `"}]},"environment":{}}}}`)
}

func localNotebook(content string) LocalItem {
	return LocalItem{Type: "Notebook", DisplayName: "NB_Load",
		Parts: []Part{{Path: "notebook-content.py", Content: []byte(content)}}}
}

func deployedPy(content string) *fabric.Definition {
	return &fabric.Definition{Parts: []fabric.DefinitionPart{{
		Path:        "notebook-content.py",
		Payload:     base64.StdEncoding.EncodeToString([]byte(content)),
		PayloadType: "InlineBase64",
	}}}
}

func TestRestoreNotebookDependencies(t *testing.T) {
	cases := []struct {
		name      string
		ipynb     []byte
		fetchErr  error
		wantDiffs int
	}{
		{"same binding is unchanged", ipynbWith(lhBound), nil, 0},
		{"other lakehouse still diffs", ipynbWith(lhOther), nil, 1},
		{"unbound target still diffs", []byte(`{"cells":[],"metadata":{"kernel_info":{"name":"synapse_pyspark"}}}`), nil, 1},
		{"failed fetch keeps the diff", nil, errors.New("throttled"), 1},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			local := localNotebook(nbBound)
			deployed := deployedPy(nbStripped)
			calls := 0
			err := RestoreNotebookDependencies(local, deployed, func() ([]byte, error) {
				calls++
				return c.ipynb, c.fetchErr
			})
			if calls != 1 {
				t.Fatalf("ipynb fetched %d times, want 1", calls)
			}
			if !errors.Is(err, c.fetchErr) {
				t.Fatalf("err = %v, want %v", err, c.fetchErr)
			}
			localParts := map[string][]byte{"notebook-content.py": []byte(nbBound)}
			if d := DiffParts(localParts, deployed); len(d) != c.wantDiffs {
				t.Fatalf("got %d diffs, want %d", len(d), c.wantDiffs)
			}
		})
	}
}

func TestRestoreNotebookDependencies_NoFetchWhenNotNeeded(t *testing.T) {
	fail := func() ([]byte, error) { return nil, errors.New("must not fetch") }
	if err := RestoreNotebookDependencies(localNotebook(nbBound), deployedPy(nbBound), fail); err != nil {
		t.Fatalf("bound target: %v", err)
	}
	if err := RestoreNotebookDependencies(localNotebook(nbStripped), deployedPy(nbStripped), fail); err != nil {
		t.Fatalf("unbound local: %v", err)
	}
}

func TestRestoreNotebookDependencies_EnvironmentOnly(t *testing.T) {
	envOnly := nb(`# META {
# META   "kernel_info": {
# META     "name": "synapse_pyspark"
# META   },
# META   "dependencies": {
# META     "environment": {
# META       "environmentId": "cccccccc-0000-0000-0000-000000000001",
# META       "workspaceId": "` + wsBound + `"
# META     }
# META   }
# META }`)
	deployed := deployedPy(nbStripped)
	err := RestoreNotebookDependencies(localNotebook(envOnly), deployed, func() ([]byte, error) {
		return []byte(`{"cells":[],"metadata":{"kernel_info":{"name":"synapse_pyspark"},"dependencies":` +
			`{"environment":{"environmentId":"cccccccc-0000-0000-0000-000000000001","workspaceId":"` + wsBound + `"}}}}`), nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if d := DiffParts(map[string][]byte{"notebook-content.py": []byte(envOnly)}, deployed); len(d) != 0 {
		t.Fatalf("an environment-only binding must be restored too, got %d diffs", len(d))
	}
}

func TestNotebookMetaTrailingContentStillDiffs(t *testing.T) {
	trailing := nb(`# META {
# META   "kernel_info": {"name": "synapse_pyspark"}
# META }
# META {"stray": true}`)
	local := map[string][]byte{"notebook-content.py": []byte(trailing)}
	if d := DiffParts(local, deployedPy(nbStripped)); len(d) != 1 {
		t.Fatalf("content after the metadata object must not vanish from the compare, got %d diffs", len(d))
	}
}

func TestNotebookMetaCompareIgnoresKeyOrderAndIndent(t *testing.T) {
	reordered := nb(`# META {
# META     "dependencies": {"environment": {}, "lakehouse": {"known_lakehouses": [{"id": "` + lhBound + `"}],
# META       "default_lakehouse_workspace_id": "` + wsBound + `",
# META       "default_lakehouse_name": "LH_ConfigLog",
# META       "default_lakehouse": "` + lhBound + `"}},
# META     "kernel_info": {"name": "synapse_pyspark"}
# META }`)
	local := map[string][]byte{"notebook-content.py": []byte(nbBound)}
	if d := DiffParts(local, deployedPy(reordered)); len(d) != 0 {
		t.Fatalf("same metadata in another layout must not diff, got:\n--- old\n%s\n--- new\n%s", d[0].Old, d[0].New)
	}
	changed := map[string][]byte{"notebook-content.py": []byte(nbBound + "\nprint(1)\n")}
	if d := DiffParts(changed, deployedPy(reordered)); len(d) != 1 {
		t.Fatalf("a code change must diff, got %d diffs", len(d))
	}
}
