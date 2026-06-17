package fabric

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	neturl "net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

// uuidRe matches a canonical Fabric/PowerBI resource ID (workspace,
// item, dataset, request). Used by validateUUID to reject anything
// that could change URL semantics when interpolated into a path.
var uuidRe = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// validateUUID rejects IDs that aren't canonical UUIDs. Defense-in-depth:
// IDs come from Microsoft API responses today, but a corrupt config or
// MITM response could slip a path separator into a URL otherwise.
func validateUUID(id, label string) error {
	if !uuidRe.MatchString(id) {
		return fmt.Errorf("invalid %s: %q is not a valid UUID", label, id)
	}
	return nil
}

const (
	baseURL        = "https://api.fabric.microsoft.com"
	powerBIBaseURL = "https://api.powerbi.com/v1.0/myorg"
	// maxResponseSize caps any single response read at 10 MB. Definition
	// payloads are base64, so this is generous.
	maxResponseSize = 10 << 20

	maxThrottleRetries = 5
	maxThrottleWait    = 60 * time.Second
)

var httpClient = &http.Client{Timeout: 60 * time.Second}

// throttleDelay computes how long to wait before retrying a 429. It honors the
// Retry-After header (delta-seconds form) when present and positive, capped at
// maxThrottleWait; otherwise it falls back to capped exponential backoff
// (1s, 2s, 4s, …) by attempt number.
func throttleDelay(retryAfter string, attempt int) time.Duration {
	if secs, err := strconv.Atoi(strings.TrimSpace(retryAfter)); err == nil && secs > 0 {
		d := time.Duration(secs) * time.Second
		if d > maxThrottleWait {
			return maxThrottleWait
		}
		return d
	}
	d := time.Duration(1<<uint(attempt)) * time.Second
	if d > maxThrottleWait {
		return maxThrottleWait
	}
	return d
}

// Workspace is a minimal projection of the Fabric workspace resource.
type Workspace struct {
	ID          string `json:"id"`
	DisplayName string `json:"displayName"`
}

// Item is a generic Fabric item. Type is "Notebook", "SemanticModel", etc.
type Item struct {
	ID          string `json:"id"`
	DisplayName string `json:"displayName"`
	Type        string `json:"type"`
	WorkspaceID string `json:"workspaceId"`
}

// pagedGet walks a Fabric Core listing endpoint that pages via
// continuationUri, returning the accumulated Value list. Used by
// ListWorkspaces / ListItems / ListItemsByType — they all share the
// same envelope shape.
func pagedGet[T any](token, startURL, what string) ([]T, error) {
	var all []T
	url := startURL
	for url != "" {
		body, err := doGet(token, url)
		if err != nil {
			return nil, err
		}
		var page struct {
			Value           []T    `json:"value"`
			ContinuationURI string `json:"continuationUri"`
		}
		if err := json.Unmarshal(body, &page); err != nil {
			return nil, fmt.Errorf("parse %s: %w", what, err)
		}
		all = append(all, page.Value...)
		url = page.ContinuationURI
	}
	return all, nil
}

// ListWorkspaces returns every workspace the authenticated user can see,
// paging through Fabric's continuationUri internally. Used for workspace
// discovery in the customer-edit flow.
func ListWorkspaces(token string) ([]Workspace, error) {
	return pagedGet[Workspace](token, baseURL+"/v1/workspaces", "workspaces")
}

// GetWorkspaceID resolves a workspace by displayName. Thin wrapper around
// ListWorkspaces because Fabric Core doesn't support $filter on workspaces.
func GetWorkspaceID(token, name string) (string, error) {
	workspaces, err := ListWorkspaces(token)
	if err != nil {
		return "", err
	}
	for _, ws := range workspaces {
		if ws.DisplayName == name {
			return ws.ID, nil
		}
	}
	return "", fmt.Errorf("workspace %q not found (check spelling and your access)", name)
}

// ListItems returns every item in a workspace, regardless of type.
// Paging is handled internally via continuationUri. Used by the
// Move flow to show a single mixed-type picker (then filtered
// client-side to the supported subset).
func ListItems(token, workspaceID string) ([]Item, error) {
	if err := validateUUID(workspaceID, "workspace ID"); err != nil {
		return nil, err
	}
	url := fmt.Sprintf("%s/v1/workspaces/%s/items", baseURL, workspaceID)
	return pagedGet[Item](token, url, "items")
}

// ListItemsByType returns items of a specific type ("Notebook",
// "Report", "SemanticModel", etc.). Same paging as ListItems.
// Use this when you only need one type — saves filtering client-side
// when the workspace has many items of other types.
func ListItemsByType(token, workspaceID, itemType string) ([]Item, error) {
	if err := validateUUID(workspaceID, "workspace ID"); err != nil {
		return nil, err
	}
	url := fmt.Sprintf("%s/v1/workspaces/%s/items?type=%s",
		baseURL, workspaceID, itemType)
	return pagedGet[Item](token, url, "items")
}

// CreateItem creates a new item in a workspace with the given
// definition. Returns the new Item (with its ID) so callers can
// immediately follow up — most notably to rebind a freshly created
// Report. The call is async: 202 + Location is polled to
// completion. The 5-minute cap handles large report definitions
// that the default 2-minute cap would time out on.
func CreateItem(token, workspaceID, displayName, itemType string, def *Definition) (Item, error) {
	if err := validateUUID(workspaceID, "workspace ID"); err != nil {
		return Item{}, err
	}
	body := struct {
		DisplayName string     `json:"displayName"`
		Type        string     `json:"type"`
		Definition  Definition `json:"definition"`
	}{
		DisplayName: displayName,
		Type:        itemType,
		Definition:  *def,
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return Item{}, fmt.Errorf("marshal create body: %w", err)
	}

	url := fmt.Sprintf("%s/v1/workspaces/%s/items", baseURL, workspaceID)
	resultBody, err := doLRO(token, url, bytes.NewReader(payload), 150)
	if err != nil {
		return Item{}, err
	}

	// doLRO treats 204 as success; CreateItem needs a body to extract the
	// new item ID. Guard against an empty response (would otherwise fail
	// json.Unmarshal with the misleading 'unexpected end of JSON input').
	if len(resultBody) == 0 {
		return Item{}, fmt.Errorf("createItem returned empty body — operation may have completed but the new item ID is unknown; check %q in workspace manually", displayName)
	}

	var created Item
	if err := json.Unmarshal(resultBody, &created); err != nil {
		return Item{}, fmt.Errorf("parse created item: %w", err)
	}
	return created, nil
}

// UpdateItemDefinition replaces the definition of an existing item.
// Used by the move flow's "overwrite" branch so the destination
// item's ID survives — preserving incoming bindings (e.g. reports
// already pointing at this semantic model). Async same as
// CreateItem; reuses the 5-minute cap.
func UpdateItemDefinition(token, workspaceID, itemID string, def *Definition) error {
	if err := validateUUID(workspaceID, "workspace ID"); err != nil {
		return err
	}
	if err := validateUUID(itemID, "item ID"); err != nil {
		return err
	}
	body := struct {
		Definition Definition `json:"definition"`
	}{Definition: *def}
	payload, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("marshal update body: %w", err)
	}

	url := fmt.Sprintf("%s/v1/workspaces/%s/items/%s/updateDefinition",
		baseURL, workspaceID, itemID)
	_, err = doLRO(token, url, bytes.NewReader(payload), 150)
	return err
}

// ListNotebooks returns all notebook items in a workspace.
// Thin wrapper over ListItemsByType for backward compatibility.
func ListNotebooks(token, workspaceID string) ([]Item, error) {
	return ListItemsByType(token, workspaceID, "Notebook")
}

// FindNotebookByName returns the notebook with the given displayName, or error
// if zero or more than one match. Exact match, case-sensitive — same as Fabric.
func FindNotebookByName(token, workspaceID, name string) (Item, error) {
	items, err := ListNotebooks(token, workspaceID)
	if err != nil {
		return Item{}, err
	}
	var matches []Item
	for _, it := range items {
		if it.DisplayName == name {
			matches = append(matches, it)
		}
	}
	switch len(matches) {
	case 0:
		return Item{}, fmt.Errorf("notebook %q not found in workspace %s", name, workspaceID)
	case 1:
		return matches[0], nil
	default:
		return Item{}, fmt.Errorf("notebook %q is ambiguous (%d matches)", name, len(matches))
	}
}

// Definition is Fabric's item-definition envelope. Returned by
// getDefinition and accepted by createItem / updateItemDefinition.
// Each part is a base64 payload identified by a relative path
// (e.g. "notebook-content.ipynb", "report.json", "model.bim").
type Definition struct {
	Parts []DefinitionPart `json:"parts"`
}

// DefinitionPart is one file inside an item definition. PayloadType
// is almost always "InlineBase64" in practice but Fabric reserves
// other values, so callers should not assume.
type DefinitionPart struct {
	Path        string `json:"path"`
	Payload     string `json:"payload"`
	PayloadType string `json:"payloadType"`
}

// GetItemDefinition fetches the full definition of any Fabric item.
// format is forwarded as ?format=… when non-empty (notebooks need
// "ipynb"; reports and semantic models use "" for the default).
//
// Fabric's getDefinition is async: the first POST may return 202 +
// Location, which is polled to completion. The 60-attempt cap in
// pollOperation (2 minutes) is sufficient for getDefinition; large
// items are handled by the longer cap on CreateItem in Task 6.
func GetItemDefinition(token, workspaceID, itemID, format string) (*Definition, error) {
	if err := validateUUID(workspaceID, "workspace ID"); err != nil {
		return nil, err
	}
	if err := validateUUID(itemID, "item ID"); err != nil {
		return nil, err
	}
	url := fmt.Sprintf("%s/v1/workspaces/%s/items/%s/getDefinition",
		baseURL, workspaceID, itemID)
	if format != "" {
		url += "?format=" + format
	}

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
	return &wrapper.Definition, nil
}

// GetNotebookIpynb returns the raw .ipynb bytes of a notebook item.
// Thin wrapper over GetItemDefinition that extracts the .ipynb part.
func GetNotebookIpynb(token, workspaceID, itemID string) ([]byte, error) {
	def, err := GetItemDefinition(token, workspaceID, itemID, "ipynb")
	if err != nil {
		return nil, err
	}
	for _, p := range def.Parts {
		if !strings.HasSuffix(p.Path, ".ipynb") {
			continue
		}
		if p.PayloadType == "InlineBase64" || p.PayloadType == "" {
			decoded, err := base64.StdEncoding.DecodeString(p.Payload)
			if err == nil {
				return decoded, nil
			}
		}
		return []byte(p.Payload), nil
	}
	return nil, fmt.Errorf("no .ipynb part in notebook definition")
}

// ParseNotebookParameters is a convenience helper: fetches the .ipynb and
// parses its parameters cell in one call.
func ParseNotebookParameters(token, workspaceID, itemID string) ([]Parameter, error) {
	ipynb, err := GetNotebookIpynb(token, workspaceID, itemID)
	if err != nil {
		return nil, err
	}
	return ParseParameters(ipynb)
}

// pollOperation follows a Fabric long-running operation to
// completion and returns the result body. maxAttempts caps the
// number of poll cycles (each 2 seconds apart). Pass 0 to use the
// default of 60 (≈2 minutes) — enough for getDefinition; CreateItem
// uses a longer cap for large report definitions.
func pollOperation(token, operationURL string, maxAttempts int) ([]byte, error) {
	if maxAttempts <= 0 {
		maxAttempts = 60
	}
	const pollInterval = 2 * time.Second

	for attempt := 0; attempt < maxAttempts; attempt++ {
		data, err := doGet(token, operationURL)
		if err != nil {
			return nil, fmt.Errorf("poll: %w", err)
		}
		var op struct {
			Status string `json:"status"`
		}
		if err := json.Unmarshal(data, &op); err != nil {
			return nil, fmt.Errorf("parse operation: %w", err)
		}
		switch op.Status {
		case "Succeeded":
			return doGet(token, operationURL+"/result")
		case "Failed":
			return nil, fmt.Errorf("operation failed: %s", string(data))
		}
		time.Sleep(pollInterval)
	}
	return nil, fmt.Errorf("operation did not complete within %d attempts", maxAttempts)
}

// userAgent is sent on every Fabric/PowerBI request. Microsoft asks
// public-client integrations to identify themselves for diagnostics;
// the version is set by main.go from the release build's -ldflags.
var userAgent = "futils/dev"

// SetUserAgent lets main.go install the build version. Called at
// startup before any HTTP traffic.
func SetUserAgent(v string) {
	if v != "" {
		userAgent = "futils/" + v
	}
}

// currentProfile is the auth profile to refresh against when an HTTP
// call returns 401 mid-flow. GetAccessToken sets it as a side effect
// so callers don't have to thread the profile through every wrapper /
// poller. RWMutex because in principle two flows could run in parallel
// (we don't today, but a hung shutdown + tests is enough to want it).
var (
	currentProfileMu sync.RWMutex
	currentProfile   string
)

// SetProfile remembers which profile to refresh against on a 401 retry.
// Called automatically by GetAccessToken — manual callers shouldn't need
// to touch it.
func SetProfile(p string) {
	currentProfileMu.Lock()
	currentProfile = p
	currentProfileMu.Unlock()
}

func getProfile() string {
	currentProfileMu.RLock()
	defer currentProfileMu.RUnlock()
	return currentProfile
}

func authHeader(token string) http.Header {
	h := http.Header{}
	h.Set("Authorization", "Bearer "+token)
	h.Set("User-Agent", userAgent)
	return h
}

// retryWithFreshToken mints a new access token via the cached/refresh
// path and returns it for the caller to retry the request. Returns
// ("", false) if there's no remembered profile (i.e. GetAccessToken
// was never called) or the refresh fails — both cases mean "let the
// 401 bubble up to the user."
func retryWithFreshToken() (string, bool) {
	profile := getProfile()
	if profile == "" {
		return "", false
	}
	// loadCachedToken first — if the keyring already has a non-expired
	// token (e.g. a parallel flow already refreshed) we use that and
	// avoid an unnecessary refresh-token round-trip.
	if token, ok := loadCachedToken(profile); ok {
		return token, true
	}
	if token, ok := refreshAccessToken(profile); ok {
		return token, true
	}
	return "", false
}

func doGet(token, rawURL string) ([]byte, error) {
	// Quote-escape any spaces an operator may have left in displayName filters.
	if parsed, err := neturl.Parse(rawURL); err == nil {
		rawURL = parsed.String()
	}
	for attempt := 0; ; attempt++ {
		body, status, retryAfter, err := doGetOnce(token, rawURL)
		if err != nil {
			return nil, err
		}
		// On 401, the token captured at flow start has likely expired during
		// a long-running poll. Mint a fresh one via cached/refresh-grant
		// (browser auth is NOT triggered here — that requires interactive
		// terminal context) and retry once.
		if status == http.StatusUnauthorized {
			if fresh, ok := retryWithFreshToken(); ok {
				body, status, retryAfter, err = doGetOnce(fresh, rawURL)
				if err != nil {
					return nil, err
				}
			}
		}
		if status == http.StatusTooManyRequests && attempt < maxThrottleRetries {
			time.Sleep(throttleDelay(retryAfter, attempt))
			continue
		}
		if status >= 400 {
			return nil, fmt.Errorf("GET %s → %d: %s", rawURL, status, string(body))
		}
		return body, nil
	}
}

func doGetOnce(token, rawURL string) ([]byte, int, string, error) {
	req, err := http.NewRequest("GET", rawURL, nil)
	if err != nil {
		return nil, 0, "", fmt.Errorf("invalid URL: %w", err)
	}
	req.Header = authHeader(token)
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, 0, "", err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseSize))
	if err != nil {
		return nil, resp.StatusCode, "", fmt.Errorf("read body: %w", err)
	}
	return body, resp.StatusCode, resp.Header.Get("Retry-After"), nil
}

// doLRO is the long-running-operation primitive used by CreateItem,
// UpdateItemDefinition, GetItemDefinition, and QueryRefreshableTables.
// It POSTs the request, follows a 202+Location to completion via
// pollOperation, or returns the body directly on a 2xx. maxAttempts
// forwards to pollOperation (0 = default 60 attempts × 2s ≈ 2 min);
// pass 150 (~5 min) for create/update on large definitions.
func doLRO(token, rawURL string, reqBody io.Reader, maxAttempts int) ([]byte, error) {
	resp, body, err := doPost(token, rawURL, reqBody)
	if err != nil {
		return nil, err
	}
	switch resp.StatusCode {
	case 200, 201, 204:
		return body, nil
	case 202:
		loc := resp.Header.Get("Location")
		if loc == "" {
			return nil, fmt.Errorf("LRO 202 missing Location header")
		}
		return pollOperation(token, loc, maxAttempts)
	default:
		return nil, fmt.Errorf("LRO %s → %d: %s", rawURL, resp.StatusCode, string(body))
	}
}

func doPost(token, rawURL string, reqBody io.Reader) (*http.Response, []byte, error) {
	// Drain the body up front so the 401/429-retry path can replay it.
	// All current callers pass either nil or a bytes.NewReader, so this
	// is at worst a no-op copy.
	var bodyBytes []byte
	if reqBody != nil {
		var err error
		bodyBytes, err = io.ReadAll(reqBody)
		if err != nil {
			return nil, nil, fmt.Errorf("read request body: %w", err)
		}
	}
	for attempt := 0; ; attempt++ {
		resp, body, err := doPostOnce(token, rawURL, bodyBytes)
		if err != nil {
			return resp, body, err
		}
		if resp.StatusCode == http.StatusUnauthorized {
			if fresh, ok := retryWithFreshToken(); ok {
				resp, body, err = doPostOnce(fresh, rawURL, bodyBytes)
				if err != nil {
					return resp, body, err
				}
			}
		}
		if resp.StatusCode == http.StatusTooManyRequests && attempt < maxThrottleRetries {
			time.Sleep(throttleDelay(resp.Header.Get("Retry-After"), attempt))
			continue
		}
		return resp, body, nil
	}
}

func doPostOnce(token, rawURL string, bodyBytes []byte) (*http.Response, []byte, error) {
	var reader io.Reader
	if bodyBytes != nil {
		reader = bytes.NewReader(bodyBytes)
	}
	req, err := http.NewRequest("POST", rawURL, reader)
	if err != nil {
		return nil, nil, fmt.Errorf("invalid URL: %w", err)
	}
	req.Header = authHeader(token)
	if bodyBytes != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseSize))
	if err != nil {
		return nil, nil, fmt.Errorf("read body: %w", err)
	}
	return resp, body, nil
}

// JobInput is one parameter sent to a RunNotebook job. Type must be one of
// the fabric.Type* constants ("string", "bool", "int", "float"). Value is
// a typed Go value — marshalled as-is, so use string for "string", bool
// for "bool", int64/int for "int", float64 for "float".
//
// Name is not serialised here — the wire format keys parameters by name
// using the Name as the map key (see paramValue + RunNotebook body).
type JobInput struct {
	Name  string `json:"-"`
	Value any    `json:"value"`
	Type  string `json:"type"`
}

// paramValue is the per-parameter shape the Fabric RunNotebook job accepts:
// {"value": <typed>, "type": "string|bool|int|float"}. Parameters are keyed
// by name in a map under executionData.parameters.
type paramValue struct {
	Value any    `json:"value"`
	Type  string `json:"type"`
}

// DefaultLakehouse is the per-run lakehouse override sent under
// executionData.configuration.defaultLakehouse. Supplying it mounts the named
// lakehouse for this one job, overriding whatever (possibly broken) binding
// the notebook carries in its own metadata. Used to repair notebooks that pin
// a lakehouse GUID but lost the workspace id — see ResolveDefaultLakehouse in
// the run flow. Shape mirrors fabric-cli / the %%configure magic
// ({name, id, workspaceId}); the generic REST reference treats executionData
// as job-type-opaque, so this is the notebook-job-specific schema in practice.
type DefaultLakehouse struct {
	Name        string `json:"name,omitempty"`
	ID          string `json:"id"`
	WorkspaceID string `json:"workspaceId"`
}

// runConfiguration is the executionData.configuration block. Only the fields
// futils sets are modelled; omitempty keeps it absent unless a value is set.
type runConfiguration struct {
	DefaultLakehouse *DefaultLakehouse `json:"defaultLakehouse,omitempty"`
}

// runBody is the full RunNotebook request body.
type runBody struct {
	ExecutionData struct {
		Parameters    map[string]paramValue `json:"parameters,omitempty"`
		Configuration *runConfiguration     `json:"configuration,omitempty"`
	} `json:"executionData"`
}

// buildRunBody assembles (and marshals) the RunNotebook request body from the
// per-run parameter overrides and an optional default-lakehouse override.
// Extracted from RunNotebook so the wire shape is unit-testable without HTTP.
func buildRunBody(inputs []JobInput, lakehouse *DefaultLakehouse) ([]byte, error) {
	var body runBody
	if len(inputs) > 0 {
		params := make(map[string]paramValue, len(inputs))
		for _, in := range inputs {
			params[in.Name] = paramValue{Value: in.Value, Type: in.Type}
		}
		body.ExecutionData.Parameters = params
	}
	if lakehouse != nil {
		body.ExecutionData.Configuration = &runConfiguration{DefaultLakehouse: lakehouse}
	}
	return json.Marshal(body)
}

// RunNotebook triggers an on-demand notebook job and returns the job
// instance URL (from the 202 Location header) so callers can poll for
// completion.
//
// Uses the generic Core endpoint `/items/{id}/jobs/instances?jobType=RunNotebook`
// — the same one Microsoft's own fabric-cli uses. The release-format
// `/notebooks/{id}/jobs/execute/instances` endpoint accepts the request
// (returns 202) but silently ignores parameters, at least as of April 2026.
//
// Body shape (from fabric-cli's ITJobMap comment in fab_types.py):
//
//	{
//	  "executionData": {
//	    "parameters": {
//	      "param_name": {"value": <typed>, "type": "string|bool|int|float"}
//	    },
//	    "configuration": {
//	      "defaultLakehouse": {"name": ..., "id": ..., "workspaceId": ...}
//	    }
//	  }
//	}
//
// lakehouse is optional: pass nil to leave the notebook's own metadata
// binding untouched (the normal case), or a resolved DefaultLakehouse to
// override the session's default lakehouse for this run — used to repair
// notebooks whose binding pins a lakehouse but lost its workspace id.
func RunNotebook(token, workspaceID, itemID string, inputs []JobInput, lakehouse *DefaultLakehouse) (string, error) {
	if err := validateUUID(workspaceID, "workspace ID"); err != nil {
		return "", err
	}
	if err := validateUUID(itemID, "item ID"); err != nil {
		return "", err
	}

	payload, err := buildRunBody(inputs, lakehouse)
	if err != nil {
		return "", fmt.Errorf("marshal run body: %w", err)
	}

	url := fmt.Sprintf("%s/v1/workspaces/%s/items/%s/jobs/instances?jobType=RunNotebook",
		baseURL, workspaceID, itemID)
	resp, respBody, err := doPost(token, url, bytes.NewReader(payload))
	if err != nil {
		return "", err
	}
	if resp.StatusCode != 202 {
		return "", fmt.Errorf("RunNotebook %d: %s", resp.StatusCode, string(respBody))
	}
	loc := resp.Header.Get("Location")
	if loc == "" {
		return "", fmt.Errorf("RunNotebook 202 missing Location header")
	}
	return loc, nil
}

// JobInstanceStatus is the minimal shape we care about when polling a job.
type JobInstanceStatus struct {
	Status         string `json:"status"`
	StartTimeUtc   string `json:"startTimeUtc"`
	EndTimeUtc     string `json:"endTimeUtc"`
	FailureReason  any    `json:"failureReason"`
	RootActivityID string `json:"rootActivityId"`
}

// Job instance status values returned by Fabric. The first four are
// terminal; anything else means "still running".
const (
	JobStatusCompleted = "Completed"
	JobStatusFailed    = "Failed"
	JobStatusCancelled = "Cancelled"
	JobStatusDeduped   = "Deduped"
)

// IsTerminal reports whether a JobInstanceStatus has reached a state
// where further polling is pointless.
func (s JobInstanceStatus) IsTerminal() bool {
	switch s.Status {
	case JobStatusCompleted, JobStatusFailed, JobStatusCancelled, JobStatusDeduped:
		return true
	}
	return false
}

// GetJobInstance fetches current status of a job instance by its URL
// (returned from RunNotebook).
func GetJobInstance(token, instanceURL string) (JobInstanceStatus, error) {
	data, err := doGet(token, instanceURL)
	if err != nil {
		return JobInstanceStatus{}, err
	}
	var s JobInstanceStatus
	if err := json.Unmarshal(data, &s); err != nil {
		return s, fmt.Errorf("parse job instance: %w", err)
	}
	return s, nil
}

// GetLakehouseSqlEndpoint returns the SQL analytics endpoint (host, id) of a
// lakehouse. Used to resolve fabric-cicd's $sqlendpoint / $sqlendpointid
// dynamic variables during deployment.
func GetLakehouseSqlEndpoint(token, workspaceID, lakehouseID string) (string, string, error) {
	if err := validateUUID(workspaceID, "workspace ID"); err != nil {
		return "", "", err
	}
	if err := validateUUID(lakehouseID, "lakehouse ID"); err != nil {
		return "", "", err
	}
	url := fmt.Sprintf("%s/v1/workspaces/%s/items/%s", baseURL, workspaceID, lakehouseID)
	body, err := doGet(token, url)
	if err != nil {
		return "", "", err
	}
	return parseLakehouseSqlEndpoint(body)
}

func parseLakehouseSqlEndpoint(body []byte) (string, string, error) {
	var resp struct {
		Properties struct {
			SQLEndpointProperties struct {
				ConnectionString string `json:"connectionString"`
				ID               string `json:"id"`
			} `json:"sqlEndpointProperties"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return "", "", fmt.Errorf("parse lakehouse: %w", err)
	}
	host := resp.Properties.SQLEndpointProperties.ConnectionString
	id := resp.Properties.SQLEndpointProperties.ID
	if host == "" {
		return "", "", fmt.Errorf("lakehouse has no SQL endpoint yet (still provisioning?)")
	}
	return host, id, nil
}

// RebindReport repoints a Report at a different semantic model
// (dataset). This is the Power BI REST API, not Fabric Core — the
// base URL is api.powerbi.com. As of 2026-05 the call accepts the
// same access token we use for Fabric, but Microsoft documents the
// Power BI audience separately; if rebind starts returning 401/403,
// the fix is to expand the requested scopes in auth.go to include
// "https://analysis.windows.net/powerbi/api/.default".
//
// Returns nil on success. Rebind is synchronous — no polling.
func RebindReport(token, workspaceID, reportID, datasetID string) error {
	if err := validateUUID(workspaceID, "workspace ID"); err != nil {
		return err
	}
	if err := validateUUID(reportID, "report ID"); err != nil {
		return err
	}
	if err := validateUUID(datasetID, "dataset ID"); err != nil {
		return err
	}
	body := struct {
		DatasetID string `json:"datasetId"`
	}{DatasetID: datasetID}
	payload, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("marshal rebind body: %w", err)
	}

	url := fmt.Sprintf("%s/groups/%s/reports/%s/Rebind",
		powerBIBaseURL, workspaceID, reportID)
	resp, respBody, err := doPost(token, url, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	switch resp.StatusCode {
	case 200, 204:
		return nil
	default:
		return fmt.Errorf("rebind %d: %s", resp.StatusCode, string(respBody))
	}
}
