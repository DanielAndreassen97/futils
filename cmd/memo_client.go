package cmd

import (
	"sync"

	"github.com/DanielAndreassen97/futils/internal/fabric"
)

// memoClient wraps an APIClient and memoizes ListItems results by workspaceID.
// All other APIClient methods are promoted via the embedded field.
//
// The cache is correct because all ListItems calls in a deploy run happen in
// the compare/index phase (before Execute or DeleteItems mutate the workspace),
// so there are no post-mutation callers that need a fresh list.
//
// Thread-safety: the RWMutex lets concurrent readers share the lock once a
// workspace is cached. BuildNameIndex could be parallelized in the future, so
// we make it safe now.
type memoClient struct {
	APIClient
	mu    sync.RWMutex
	cache map[string][]fabric.Item // workspaceID -> items
}

// newMemoClient returns a memoClient that wraps inner. The cache is scoped to
// this instance's lifetime — wire one per deploy run so it expires with the run.
func newMemoClient(inner APIClient) *memoClient {
	return &memoClient{
		APIClient: inner,
		cache:     make(map[string][]fabric.Item),
	}
}

// ListItems returns the workspace's items, hitting the inner client at most once
// per workspaceID. Subsequent calls for the same workspaceID are served from cache.
func (m *memoClient) ListItems(token, workspaceID string) ([]fabric.Item, error) {
	// Fast path: read lock, return if cached.
	m.mu.RLock()
	if items, ok := m.cache[workspaceID]; ok {
		m.mu.RUnlock()
		return items, nil
	}
	m.mu.RUnlock()

	// Slow path: fetch from inner, then write into cache.
	// Double-check after acquiring the write lock to handle a concurrent miss.
	m.mu.Lock()
	defer m.mu.Unlock()
	if items, ok := m.cache[workspaceID]; ok {
		return items, nil // another goroutine beat us to it
	}
	items, err := m.APIClient.ListItems(token, workspaceID)
	if err != nil {
		return nil, err
	}
	m.cache[workspaceID] = items
	return items, nil
}
