package fabric

import "testing"

func TestParseLakehouseSqlEndpoint(t *testing.T) {
	body := []byte(`{
	  "id": "lh-1",
	  "properties": {
	    "sqlEndpointProperties": {
	      "connectionString": "abc.datawarehouse.fabric.microsoft.com",
	      "id": "ep-123"
	    }
	  }
	}`)
	host, id, err := parseLakehouseSqlEndpoint(body)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if host != "abc.datawarehouse.fabric.microsoft.com" || id != "ep-123" {
		t.Errorf("host=%q id=%q", host, id)
	}
}

func TestParseLakehouseSqlEndpointMissing(t *testing.T) {
	if _, _, err := parseLakehouseSqlEndpoint([]byte(`{"id":"lh","properties":{}}`)); err == nil {
		t.Fatal("expected error when sql endpoint absent")
	}
}
