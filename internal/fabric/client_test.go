package fabric

import (
	"testing"
	"time"
)

func TestThrottleDelayHonorsRetryAfter(t *testing.T) {
	if got := throttleDelay("10", 0); got != 10*time.Second {
		t.Errorf("got %v, want 10s", got)
	}
}

func TestNextPollIntervalBacksOff(t *testing.T) {
	// Floor matches the reference dry-run's steady 1s poll so we don't spike the
	// request rate past Fabric's limit; it then backs off to the 2s cap.
	cases := []struct{ prev, want time.Duration }{
		{0, 1 * time.Second},               // first wait after an immediate poll miss
		{1 * time.Second, 2 * time.Second}, // back off toward the cap
		{2 * time.Second, 2 * time.Second}, // capped
		{5 * time.Second, 2 * time.Second}, // never exceeds the cap
	}
	for _, c := range cases {
		if got := nextPollInterval(c.prev); got != c.want {
			t.Errorf("nextPollInterval(%v) = %v, want %v", c.prev, got, c.want)
		}
	}
}

func TestThrottleDelayCapsRetryAfter(t *testing.T) {
	if got := throttleDelay("9999", 0); got != maxThrottleWait {
		t.Errorf("got %v, want %v", got, maxThrottleWait)
	}
}

func TestThrottleDelayBackoffWithoutHeader(t *testing.T) {
	if got := throttleDelay("", 2); got != 4*time.Second { // 1<<2
		t.Errorf("got %v, want 4s", got)
	}
}

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
