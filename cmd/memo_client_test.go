package cmd

import (
	"testing"

	"github.com/DanielAndreassen97/futils/internal/fabric"
)

// countingClient wraps a base APIClient and counts how many times ListItems
// is called per workspaceID. Used to verify the memo decorator's dedup.
type countingClient struct {
	APIClient
	calls map[string]int // workspaceID -> call count
	items map[string][]fabric.Item
}

func newCountingClient(items map[string][]fabric.Item) *countingClient {
	return &countingClient{
		// Embed a nil APIClient — the decorator under test only calls ListItems
		// on the inner client, and all other methods are forwarded by the memo.
		// If a non-ListItems method is called it will panic, making the bug
		// immediately visible.
		APIClient: nil,
		calls:     map[string]int{},
		items:     items,
	}
}

func (c *countingClient) ListItems(token, workspaceID string) ([]fabric.Item, error) {
	c.calls[workspaceID]++
	return c.items[workspaceID], nil
}

// TestMemoClientDeduplicatesListItems is the RED→GREEN test.
// Without the decorator the counting client is called multiple times.
// With newMemoClient wrapping it, each workspace is listed exactly once.
func TestMemoClientDeduplicatesListItems(t *testing.T) {
	ws1Items := []fabric.Item{{ID: "item-1", DisplayName: "Item One", Type: "Notebook"}}
	ws2Items := []fabric.Item{{ID: "item-2", DisplayName: "Item Two", Type: "Report"}}
	inner := newCountingClient(map[string][]fabric.Item{
		"ws1": ws1Items,
		"ws2": ws2Items,
	})

	memo := newMemoClient(inner)

	// First call for ws1 — hits the inner client.
	got1a, err := memo.ListItems("tok", "ws1")
	if err != nil {
		t.Fatalf("first ListItems(ws1): %v", err)
	}
	if len(got1a) != 1 || got1a[0].ID != "item-1" {
		t.Fatalf("first ListItems(ws1) = %v, want [{item-1}]", got1a)
	}

	// Second call for ws1 — must be served from cache, not a new inner call.
	got1b, err := memo.ListItems("tok", "ws1")
	if err != nil {
		t.Fatalf("second ListItems(ws1): %v", err)
	}
	if len(got1b) != 1 || got1b[0].ID != "item-1" {
		t.Fatalf("second ListItems(ws1) = %v, want [{item-1}]", got1b)
	}
	if inner.calls["ws1"] != 1 {
		t.Errorf("ws1: inner called %d time(s), want exactly 1", inner.calls["ws1"])
	}

	// ws2 is a different key — must call through to the inner client independently.
	got2, err := memo.ListItems("tok", "ws2")
	if err != nil {
		t.Fatalf("ListItems(ws2): %v", err)
	}
	if len(got2) != 1 || got2[0].ID != "item-2" {
		t.Fatalf("ListItems(ws2) = %v, want [{item-2}]", got2)
	}
	if inner.calls["ws2"] != 1 {
		t.Errorf("ws2: inner called %d time(s), want exactly 1", inner.calls["ws2"])
	}

	// A third call for ws1 still comes from cache (inner still at 1).
	got1c, err := memo.ListItems("tok", "ws1")
	if err != nil {
		t.Fatalf("third ListItems(ws1): %v", err)
	}
	if len(got1c) != 1 || got1c[0].ID != "item-1" {
		t.Fatalf("third ListItems(ws1) = %v, want [{item-1}]", got1c)
	}
	if inner.calls["ws1"] != 1 {
		t.Errorf("ws1 after third call: inner called %d time(s), want exactly 1", inner.calls["ws1"])
	}
}

// TestMemoClientReturnsSameSlice ensures the cached items are identical to
// what the inner client returned — no silent transformation.
func TestMemoClientReturnsSameSlice(t *testing.T) {
	items := []fabric.Item{
		{ID: "a", DisplayName: "Alpha", Type: "Notebook"},
		{ID: "b", DisplayName: "Beta", Type: "Report"},
	}
	inner := newCountingClient(map[string][]fabric.Item{"ws": items})
	memo := newMemoClient(inner)

	first, _ := memo.ListItems("tok", "ws")
	second, _ := memo.ListItems("tok", "ws")

	if len(first) != len(items) || len(second) != len(items) {
		t.Fatalf("item count mismatch: want %d, got first=%d second=%d", len(items), len(first), len(second))
	}
	for i := range items {
		if first[i].ID != items[i].ID || second[i].ID != items[i].ID {
			t.Errorf("item[%d] mismatch: want %q, got first=%q second=%q", i, items[i].ID, first[i].ID, second[i].ID)
		}
	}
}
