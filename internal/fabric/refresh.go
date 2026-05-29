package fabric

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"sort"
	"strings"
	"time"
)

// partitionTypeM is the TMDL partition kind for M (Power Query) import
// tables — the only kind the Enhanced Refresh API can actually refresh.
// Calculated tables, calculation groups, and measure-only tables either
// have a different partition type or no partition declaration at all,
// so we filter on this string to exclude them.
const partitionTypeM = "m"

// Dataset is a Power BI semantic model (dataset) as returned by the
// Power BI /datasets endpoint. Fabric ItemsByType("SemanticModel")
// returns Items, which we adapt to this shape in ListDatasets so the
// refresh flow stays decoupled from the generic Item type.
type Dataset struct {
	ID   string
	Name string
}

// ListDatasets returns all semantic models in a workspace via Fabric
// ListItemsByType. We funnel through the Fabric Items API rather than
// the Power BI /datasets endpoint so the same Fabric token + workspace
// resolution path is reused; Fabric guarantees SemanticModel.ID equals
// the Power BI dataset ID for downstream refresh calls.
func ListDatasets(token, workspaceID string) ([]Dataset, error) {
	items, err := ListItemsByType(token, workspaceID, "SemanticModel")
	if err != nil {
		return nil, err
	}
	out := make([]Dataset, 0, len(items))
	for _, it := range items {
		out = append(out, Dataset{ID: it.ID, Name: it.DisplayName})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// QueryRefreshableTables returns the names of tables in a semantic model
// that the Enhanced Refresh API can actually process. We fetch the model's
// TMDL definition via Fabric getDefinition, then keep only tables with
// `partition X = m` — that's the M / Power Query partition type, the only
// kind Enhanced Refresh accepts. Calculated tables, calculation groups,
// and measure-only tables are silently dropped.
func QueryRefreshableTables(token, workspaceID, datasetID string) ([]string, error) {
	if err := validateUUID(workspaceID, "workspace ID"); err != nil {
		return nil, err
	}
	if err := validateUUID(datasetID, "dataset ID"); err != nil {
		return nil, err
	}

	url := fmt.Sprintf("%s/v1/workspaces/%s/semanticModels/%s/getDefinition",
		baseURL, workspaceID, datasetID)
	body, err := doLRO(token, url, nil, 0)
	if err != nil {
		return nil, err
	}

	var wrapper struct {
		Definition Definition `json:"definition"`
	}
	if err := json.Unmarshal(body, &wrapper); err != nil {
		return nil, fmt.Errorf("parse definition: %w", err)
	}

	var tables []string
	for _, part := range wrapper.Definition.Parts {
		if !strings.Contains(part.Path, "/tables/") || !strings.HasSuffix(part.Path, ".tmdl") {
			continue
		}
		content := part.Payload
		// Fabric returns InlineBase64 in practice. If a single part fails
		// to decode (server-side encoding oddity, truncated payload), log
		// it and skip — don't abort the whole listing. One bad table
		// shouldn't block the user from refreshing the other 49 healthy
		// ones.
		if part.PayloadType == "InlineBase64" || part.PayloadType == "" {
			decoded, err := base64.StdEncoding.DecodeString(content)
			if err != nil {
				fmt.Fprintf(os.Stderr, "warning: skipping TMDL part %q (decode failed: %v)\n", part.Path, err)
				continue
			}
			content = string(decoded)
		}
		name, kind := parseTMDLPartition(content)
		if name != "" && kind == partitionTypeM {
			tables = append(tables, name)
		}
	}
	sort.Strings(tables)
	return tables, nil
}

// parseTMDLPartition does a single-pass scan of a TMDL `.tmdl` file looking
// for the `table 'X'` declaration and the `partition Y = z` lines.
// Returns empty strings for files that don't contain either — fine, the
// caller filters them out.
//
// Partition kind is the first whitespace-delimited token after `=` (so
// `partition Foo = m AnnotationName=...` correctly classifies as kind "m",
// not "m annotationname=..."). Comparison is case-insensitive.
//
// If a table has multiple partition declarations (legal in TMDL for
// hybrid / composite / dual-storage tables), we accept the table if ANY
// partition is M — Enhanced Refresh handles the M side and ignores the
// rest, so reporting the table as refreshable is correct.
//
// Table-name extraction handles both `table 'My Table'` and
// `table 'My Table' annotation: ...` by extracting the content between
// the first pair of single quotes.
func parseTMDLPartition(content string) (tableName, partitionKind string) {
	for _, line := range strings.Split(content, "\n") {
		trimmed := strings.TrimSpace(line)
		if tableName == "" {
			if strings.HasPrefix(trimmed, "///") {
				continue
			}
			if strings.HasPrefix(trimmed, "table ") {
				rest := strings.TrimSpace(strings.TrimPrefix(trimmed, "table "))
				if strings.HasPrefix(rest, "'") {
					if end := strings.IndexByte(rest[1:], '\''); end >= 0 {
						tableName = rest[1 : 1+end]
					}
				} else {
					if end := strings.IndexAny(rest, " \t"); end >= 0 {
						tableName = rest[:end]
					} else {
						tableName = rest
					}
				}
			}
		}
		// Take the first whitespace-delimited token after `=` as the
		// kind. Once we've seen an M partition, lock that in — a later
		// non-M partition on the same table doesn't downgrade it.
		if strings.HasPrefix(trimmed, "partition ") && partitionKind != partitionTypeM {
			if eq := strings.IndexByte(trimmed, '='); eq >= 0 {
				rest := strings.TrimSpace(trimmed[eq+1:])
				if fields := strings.Fields(rest); len(fields) > 0 {
					partitionKind = strings.ToLower(fields[0])
				}
			}
		}
	}
	return
}

// refreshBody is the JSON payload for the Enhanced Refresh API. We always
// use `type=full` + `commitMode=transactional` for the same reason frefresh
// does: anything less is a footgun (incremental needs additional config,
// non-transactional can leave the model in a partial state on failure).
type refreshBody struct {
	Type       string          `json:"type"`
	CommitMode string          `json:"commitMode"`
	Objects    []refreshObject `json:"objects,omitempty"`
}

type refreshObject struct {
	Table string `json:"table"`
}

// TriggerRefresh kicks off an Enhanced Refresh job. If tables is nil/empty,
// the entire model is refreshed; otherwise only the named tables. Returns
// the requestID, parsed from the Location header — that's the handle we
// poll PollRefreshStatus with.
func TriggerRefresh(token, workspaceID, datasetID string, tables []string) (string, error) {
	if err := validateUUID(workspaceID, "workspace ID"); err != nil {
		return "", err
	}
	if err := validateUUID(datasetID, "dataset ID"); err != nil {
		return "", err
	}

	body := refreshBody{Type: "full", CommitMode: "transactional"}
	if len(tables) > 0 {
		body.Objects = make([]refreshObject, len(tables))
		for i, t := range tables {
			body.Objects[i] = refreshObject{Table: t}
		}
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return "", fmt.Errorf("marshal refresh body: %w", err)
	}

	rawURL := fmt.Sprintf("%s/groups/%s/datasets/%s/refreshes",
		powerBIBaseURL, workspaceID, datasetID)
	resp, respBody, err := doPost(token, rawURL, bytes.NewReader(payload))
	if err != nil {
		return "", err
	}
	if resp.StatusCode >= 400 {
		return "", fmt.Errorf("refresh trigger %d: %s", resp.StatusCode, string(respBody))
	}
	loc := resp.Header.Get("Location")
	if loc == "" {
		return "", fmt.Errorf("refresh trigger missing Location header")
	}
	return parseRequestID(loc)
}

// parseRequestID pulls the trailing UUID off a refresh-status URL. The
// Enhanced Refresh API returns the full status endpoint as the Location
// header (e.g. `.../refreshes/<uuid>`); we only need the UUID for later
// polling, but we still validate it because trusting URL trailers
// blindly invites injection-flavoured bugs.
func parseRequestID(rawURL string) (string, error) {
	parts := strings.Split(strings.TrimRight(rawURL, "/"), "/")
	id := parts[len(parts)-1]
	// Strip any query string the server might have stuck on.
	if u, err := url.Parse(id); err == nil && u.Path != "" {
		id = u.Path
	}
	if err := validateUUID(id, "request ID"); err != nil {
		return "", fmt.Errorf("unexpected Location header %q: %w", rawURL, err)
	}
	return id, nil
}

// RefreshStatus mirrors the subset of the Enhanced Refresh status payload
// the UI actually surfaces. Messages are populated on Failed/Cancelled so
// the user has a hint about what blew up.
type RefreshStatus struct {
	Status   string           `json:"status"`
	Messages []RefreshMessage `json:"messages,omitempty"`
}

type RefreshMessage struct {
	Message string `json:"message"`
}

// PollRefreshStatus fetches the current status of a refresh request. One
// call, no polling — callers compose this into a loop or use WaitForRefresh.
func PollRefreshStatus(token, workspaceID, datasetID, requestID string) (RefreshStatus, error) {
	if err := validateUUID(workspaceID, "workspace ID"); err != nil {
		return RefreshStatus{}, err
	}
	if err := validateUUID(datasetID, "dataset ID"); err != nil {
		return RefreshStatus{}, err
	}
	if err := validateUUID(requestID, "request ID"); err != nil {
		return RefreshStatus{}, err
	}
	rawURL := fmt.Sprintf("%s/groups/%s/datasets/%s/refreshes/%s",
		powerBIBaseURL, workspaceID, datasetID, requestID)
	body, err := doGet(token, rawURL)
	if err != nil {
		return RefreshStatus{}, err
	}
	var status RefreshStatus
	if err := json.Unmarshal(body, &status); err != nil {
		return RefreshStatus{}, fmt.Errorf("parse refresh status: %w", err)
	}
	return status, nil
}

// WaitForRefresh polls until the refresh reaches a terminal state or the
// timeout expires. 5s interval + 30min timeout matches frefresh's defaults
// — long-running models can take 20+ minutes, so the timeout is generous.
//
// Transient PollRefreshStatus errors (network blip, single 5xx) are
// tolerated up to maxTransient consecutive failures before giving up.
// The refresh continues server-side regardless of polling outcome; a
// single dropped packet shouldn't abort a 30-minute wait.
func WaitForRefresh(token, workspaceID, datasetID, requestID string, pollInterval, timeout time.Duration) (RefreshStatus, error) {
	const maxTransient = 3
	deadline := time.Now().Add(timeout)
	transient := 0
	var lastStatus RefreshStatus
	for time.Now().Before(deadline) {
		status, err := PollRefreshStatus(token, workspaceID, datasetID, requestID)
		if err != nil {
			transient++
			if transient > maxTransient {
				return lastStatus, err
			}
			time.Sleep(pollInterval)
			continue
		}
		transient = 0
		lastStatus = status
		switch status.Status {
		case "Completed", "Failed", "Cancelled", "Disabled":
			return status, nil
		}
		time.Sleep(pollInterval)
	}
	return RefreshStatus{}, fmt.Errorf("refresh did not complete within %s", timeout)
}

// validateUUID and uuidRe moved to client.go so client.go's URL-building
// functions can also use them. Defense-in-depth: every ID interpolated
// into a Fabric/PowerBI URL gets validated before fmt.Sprintf.
