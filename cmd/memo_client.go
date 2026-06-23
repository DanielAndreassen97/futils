package cmd

import (
	"sync"

	"github.com/DanielAndreassen97/futils/internal/fabric"
)

// memoClient wraps an APIClient and memoizes ListItems results by workspaceID.
// Every other method is forwarded to the underlying client unchanged.
//
// The cache is correct because all ListItems calls in a deploy run happen in
// the compare/index phase (before Execute or DeleteItems mutate the workspace),
// so there are no post-mutation callers that need a fresh list.
//
// Thread-safety: the RWMutex lets concurrent readers share the lock once a
// workspace is cached. BuildNameIndex could be parallelized in the future, so
// we make it safe now.
type memoClient struct {
	inner APIClient
	mu    sync.RWMutex
	cache map[string][]fabric.Item // workspaceID -> items
}

// newMemoClient returns a memoClient that wraps inner. The cache is scoped to
// this instance's lifetime — wire one per deploy run so it expires with the run.
func newMemoClient(inner APIClient) *memoClient {
	return &memoClient{
		inner: inner,
		cache: make(map[string][]fabric.Item),
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
	items, err := m.inner.ListItems(token, workspaceID)
	if err != nil {
		return nil, err
	}
	m.cache[workspaceID] = items
	return items, nil
}

// All remaining APIClient methods forward verbatim to the inner client.

func (m *memoClient) GetAccessToken(profile string) (string, error) {
	return m.inner.GetAccessToken(profile)
}
func (m *memoClient) GetWorkspaceID(token, workspaceName string) (string, error) {
	return m.inner.GetWorkspaceID(token, workspaceName)
}
func (m *memoClient) ListWorkspaces(token string) ([]fabric.Workspace, error) {
	return m.inner.ListWorkspaces(token)
}
func (m *memoClient) ListNotebooks(token, workspaceID string) ([]fabric.Item, error) {
	return m.inner.ListNotebooks(token, workspaceID)
}
func (m *memoClient) GetNotebookIpynb(token, workspaceID, itemID string) ([]byte, error) {
	return m.inner.GetNotebookIpynb(token, workspaceID, itemID)
}
func (m *memoClient) RunNotebook(token, workspaceID, itemID string, inputs []fabric.JobInput, lakehouse *fabric.DefaultLakehouse) (string, error) {
	return m.inner.RunNotebook(token, workspaceID, itemID, inputs, lakehouse)
}
func (m *memoClient) GetJobInstance(token, instanceURL string) (fabric.JobInstanceStatus, error) {
	return m.inner.GetJobInstance(token, instanceURL)
}
func (m *memoClient) ListItemsByType(token, workspaceID, itemType string) ([]fabric.Item, error) {
	return m.inner.ListItemsByType(token, workspaceID, itemType)
}
func (m *memoClient) GetItemDefinition(token, workspaceID, itemID, format string) (*fabric.Definition, error) {
	return m.inner.GetItemDefinition(token, workspaceID, itemID, format)
}
func (m *memoClient) CreateItem(token, workspaceID, displayName, itemType string, def *fabric.Definition) (fabric.Item, error) {
	return m.inner.CreateItem(token, workspaceID, displayName, itemType, def)
}
func (m *memoClient) UpdateItemDefinition(token, workspaceID, itemID string, def *fabric.Definition) error {
	return m.inner.UpdateItemDefinition(token, workspaceID, itemID, def)
}
func (m *memoClient) UpdateItem(token, workspaceID, itemID, displayName, description string) error {
	return m.inner.UpdateItem(token, workspaceID, itemID, displayName, description)
}
func (m *memoClient) DeleteItem(token, workspaceID, itemID string) error {
	return m.inner.DeleteItem(token, workspaceID, itemID)
}
func (m *memoClient) RebindReport(token, workspaceID, reportID, datasetID string) error {
	return m.inner.RebindReport(token, workspaceID, reportID, datasetID)
}
func (m *memoClient) ListDatasets(token, workspaceID string) ([]fabric.Dataset, error) {
	return m.inner.ListDatasets(token, workspaceID)
}
func (m *memoClient) QueryRefreshableTables(token, workspaceID, datasetID string) ([]string, error) {
	return m.inner.QueryRefreshableTables(token, workspaceID, datasetID)
}
func (m *memoClient) TriggerRefresh(token, workspaceID, datasetID string, tables []string) (string, error) {
	return m.inner.TriggerRefresh(token, workspaceID, datasetID, tables)
}
func (m *memoClient) WaitForRefresh(token, workspaceID, datasetID, requestID string) (fabric.RefreshStatus, error) {
	return m.inner.WaitForRefresh(token, workspaceID, datasetID, requestID)
}
func (m *memoClient) GetLakehouseSqlEndpoint(token, workspaceID, lakehouseID string) (string, string, error) {
	return m.inner.GetLakehouseSqlEndpoint(token, workspaceID, lakehouseID)
}
