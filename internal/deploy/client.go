package deploy

import (
	"encoding/json"

	"github.com/DanielAndreassen97/futils/internal/fabric"
)

// FabricClient is the narrow set of Fabric operations the deploy package needs.
// It is satisfied structurally by cmd.APIClient, so the TUI passes its existing
// client straight through without an adapter.
type FabricClient interface {
	ListItems(token, workspaceID string) ([]fabric.Item, error)
	ListItemsByType(token, workspaceID, itemType string) ([]fabric.Item, error)
	ListWorkspaces(token string) ([]fabric.Workspace, error)
	GetItemDefinition(token, workspaceID, itemID, format string) (*fabric.Definition, error)
	CreateItem(token, workspaceID, displayName, itemType string, def *fabric.Definition, creationPayload json.RawMessage, folderID string) (fabric.Item, error)
	ListFolders(token, workspaceID string) ([]fabric.Folder, error)
	CreateFolder(token, workspaceID, displayName, parentFolderID string) (fabric.Folder, error)
	UpdateItemDefinition(token, workspaceID, itemID string, def *fabric.Definition) error
	UpdateItem(token, workspaceID, itemID, displayName, description string) error
	DeleteItem(token, workspaceID, itemID string) error
	RebindReport(token, workspaceID, reportID, datasetID string) error
	BulkImportDefinitions(token, workspaceID string, parts []fabric.DefinitionPart, opts fabric.BulkImportOptions) (*fabric.BulkImportResult, error)
	GetLakehouseSqlEndpoint(token, workspaceID, lakehouseID string) (host, id string, err error)
}
