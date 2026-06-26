// internal/schemacompare/onelake_test.go
package schemacompare

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestClientListSchemas(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.URL.Path, "/unity-catalog/schemas") {
			t.Errorf("unexpected path %q", r.URL.Path)
		}
		w.Write([]byte(`{"schemas":[{"name":"dbo"},{"name":"Dim"}],"next_page_token":null}`))
	}))
	defer srv.Close()

	c := NewClientWithBase("tok", srv.URL)
	got, err := c.ListSchemas("ws", "lh")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 2 || got[0] != "Dim" || got[1] != "dbo" {
		t.Errorf("ListSchemas = %v, want [Dim dbo]", got)
	}
}

func TestClientGetTableParsesColumns(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"columns":[
			{"name":"id","type_name":"string","type_precision":0,"type_scale":0,"nullable":true,"position":0},
			{"name":"amount","type_name":"decimal","type_precision":18,"type_scale":2,"nullable":false,"position":1}
		]}`))
	}))
	defer srv.Close()

	c := NewClientWithBase("tok", srv.URL)
	cols, err := c.GetTable("ws", "lh", "dbo", "t")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(cols) != 2 {
		t.Fatalf("got %d columns, want 2", len(cols))
	}
	if cols[1].Name != "amount" || cols[1].Type != "decimal(18,2)" || cols[1].Nullable {
		t.Errorf("column[1] = %+v, want amount/decimal(18,2)/nullable=false", cols[1])
	}
}

func TestClientListTablesPaginates(t *testing.T) {
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Query().Get("page_token") == "" {
			w.Write([]byte(`{"tables":[{"name":"a"}],"next_page_token":"PAGE2"}`))
			return
		}
		w.Write([]byte(`{"tables":[{"name":"b"}],"next_page_token":null}`))
	}))
	defer srv.Close()

	c := NewClientWithBase("tok", srv.URL)
	got, err := c.ListTables("ws", "lh", "dbo")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 2 || got[0] != "a" || got[1] != "b" {
		t.Errorf("ListTables = %v, want [a b]", got)
	}
	if calls != 2 {
		t.Errorf("expected 2 paged calls, got %d", calls)
	}
}

func TestClientErrorStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		w.Write([]byte(`forbidden`))
	}))
	defer srv.Close()
	c := NewClientWithBase("tok", srv.URL)
	if _, err := c.ListSchemas("ws", "lh"); err == nil {
		t.Error("expected an error on 403")
	}
}
