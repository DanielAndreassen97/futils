package fabric

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	neturl "net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
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

	// maxThrottleRetries caps 429 retries per request. The throttle backoff is
	// min(Retry-After-or-60, 10·2^attempt) (front-loaded 10/20/40s) and 8 retries
	// gives ≈7 min total under sustained throttling, close to fabric-cicd's 300s+
	// budget, so a transiently-throttled deploy doesn't abort early.
	maxThrottleRetries = 8
	maxThrottleWait    = 60 * time.Second

	// maxNetRetries caps retries of transport-level failures (connection reset,
	// EOF, timeout) on idempotent GETs. A long-running poll loop is statistically
	// certain to hit an occasional TCP reset; one blip must not abort the read.
	maxNetRetries = 3
)

var httpClient = &http.Client{Timeout: 60 * time.Second}

// sleep is the delay primitive used by the transient-network-error backoff.
// A package var so tests can stub it to run without real delays.
var sleep = time.Sleep

// netBackoff is the delay before retrying a transient GET failure: 1s, 2s, 4s,
// capped at 8s.
func netBackoff(attempt int) time.Duration {
	d := time.Second << attempt
	if d > 8*time.Second {
		d = 8 * time.Second
	}
	return d
}

// retryTokenFn is the function used by doGet/doWrite to obtain a fresh token
// after a 401. Defined as a variable so tests can inject a controlled value.
var retryTokenFn = retryWithFreshToken

// httpDebug logs every HTTP call (method, status, duration, path) so you can see
// exactly what API work futils does during a run. Enable via FUTILS_DEBUG:
//
//	FUTILS_DEBUG=1                     → log to stderr (interleaves with the TUI)
//	FUTILS_DEBUG=/tmp/futils-http.log  → log to that file (clean TUI, full log)
var (
	httpDebug bool
	debugW    io.Writer = os.Stderr
)

func init() {
	v := strings.TrimSpace(os.Getenv("FUTILS_DEBUG"))
	if v == "" {
		return
	}
	httpDebug = true
	switch v {
	case "1", "true", "yes", "stderr":
		// debugW stays os.Stderr
	default:
		if f, err := os.Create(v); err == nil {
			debugW = f
		}
	}
}

var httpCallCount int64

// HTTPCallCount returns the number of HTTP requests issued this process.
func HTTPCallCount() int64 { return atomic.LoadInt64(&httpCallCount) }

// debugMu serializes writes to debugW: logHTTP fires from up to diffConcurrency
// worker goroutines, and Fprintf isn't guaranteed atomic on a shared writer, so
// without this the debug lines interleave byte-for-byte.
var debugMu sync.Mutex

func logHTTP(method string, status int, dur time.Duration, rawURL string) {
	atomic.AddInt64(&httpCallCount, 1)
	if !httpDebug {
		return
	}
	path := strings.TrimPrefix(strings.TrimPrefix(rawURL, baseURL), powerBIBaseURL)
	debugMu.Lock()
	fmt.Fprintf(debugW, "[http] %-6s %3d %5dms  %s\n", method, status, dur.Milliseconds(), path)
	debugMu.Unlock()
}

// throttleHits is the cumulative count of 429 backoffs since process start;
// throttleActive is a gauge of requests currently sleeping on a 429. Together
// they let a UI say "we're being rate-limited right now" (active) and "this run
// hit limits N times" (cumulative) instead of showing a frozen spinner.
var (
	throttleHits   int64
	throttleActive int64
)

// throttleSt is the mutex-guarded live backoff state. A single lock acquisition
// across all three fields makes every snapshot torn-free: no reader can see
// remaining > total even if a 429 lands between two separate reads.
//
// thrDeadline stores a Go time.Time so time.Until uses the monotonic clock,
// immunising the countdown from wall-clock adjustments (NTP steps, DST changes).
var (
	throttleStMu sync.Mutex
	thrDeadline  time.Time     // monotonic; zero when no backoff is active
	thrTotal     time.Duration // duration of the current/longest backoff
	thrAttempt   int           // 1-based attempt number of current/longest backoff
)

// ThrottleHits returns the running total of 429 backoffs. Callers typically
// snapshot it before a batch and report the delta.
func ThrottleHits() int64 { return atomic.LoadInt64(&throttleHits) }

// ThrottleSnapshot returns a torn-free view of the live throttle state: active
// (from the atomic gauge), remaining (time.Until the current deadline, ≥0),
// total (the duration of the current/longest backoff), and attempt (1-based).
// All three duration/attempt fields are read under a single lock acquisition so
// the caller can never observe remaining > total.
func ThrottleSnapshot() (active int, remaining, total time.Duration, attempt int) {
	active = int(atomic.LoadInt64(&throttleActive))
	throttleStMu.Lock()
	if thrDeadline.IsZero() {
		throttleStMu.Unlock()
		return active, 0, 0, 0
	}
	rem := time.Until(thrDeadline)
	if rem < 0 {
		rem = 0
	}
	total = thrTotal
	attempt = thrAttempt
	throttleStMu.Unlock()
	return active, rem, total, attempt
}

// MaxThrottleRetries returns the cap on 429 retries so a UI can show "retry N/M"
// without hardcoding the constant.
func MaxThrottleRetries() int { return maxThrottleRetries }

// noteThrottle records the backoff deadline and metadata under the lock. It
// keeps the LONGEST active deadline so a concurrent goroutine with a shorter
// wait never clobbers a longer one already in flight.
func noteThrottle(d time.Duration, attempt int) {
	deadline := time.Now().Add(d)
	throttleStMu.Lock()
	if deadline.After(thrDeadline) {
		thrDeadline = deadline
		thrTotal = d
		thrAttempt = attempt + 1
	}
	throttleStMu.Unlock()
}

// clearThrottleState zeros the shared backoff snapshot. Called when the last
// active waiter finishes (throttleActive drops to 0) so the next 429 in a
// later deploy group starts from a clean slate rather than inheriting an
// already-expired deadline.
//
// The re-check of throttleActive under throttleStMu is intentional: between
// the atomic.AddInt64 reaching 0 and this lock acquisition, a concurrent
// worker may have started a new backoff (incrementing active and calling
// noteThrottle, which also takes throttleStMu). By re-reading active while
// holding the lock — serialized against noteThrottle — we avoid wiping a
// freshly-set deadline.
func clearThrottleState() {
	throttleStMu.Lock()
	if atomic.LoadInt64(&throttleActive) == 0 {
		thrDeadline = time.Time{}
		thrTotal = 0
		thrAttempt = 0
	}
	throttleStMu.Unlock()
}

// firstThrottle captures the details of the first 429 seen since the last reset
// so a UI can show WHY Fabric throttled — its own error body names the limit
// that was hit, which beats guessing. A mutex (not sync.Once) guards it so the
// compare can ResetThrottleFirst() per group: otherwise a later group's "first
// 429" line would misattribute the very first 429 of the whole process.
var (
	firstThrottleMu  sync.Mutex
	firstThrottle    string
	firstThrottleSet bool
)

// FirstThrottle returns the first 429's "METHOD url — Retry-After=… — <body>"
// since the last reset, or "" if none seen.
func FirstThrottle() string {
	firstThrottleMu.Lock()
	defer firstThrottleMu.Unlock()
	return firstThrottle
}

// ResetThrottleFirst clears the recorded first-429 detail so the next throttle
// is attributed to the current batch. Callers that report a per-batch throttle
// delta (the deploy compare, per group) reset before the batch starts.
func ResetThrottleFirst() {
	firstThrottleMu.Lock()
	firstThrottle = ""
	firstThrottleSet = false
	firstThrottleMu.Unlock()
}

func recordThrottle(method, rawURL, retryAfter string, body []byte) {
	firstThrottleMu.Lock()
	defer firstThrottleMu.Unlock()
	if firstThrottleSet {
		return
	}
	firstThrottleSet = true
	b := strings.TrimSpace(string(body))
	if len(b) > 240 {
		b = b[:240] + "…"
	}
	firstThrottle = fmt.Sprintf("%s %s — Retry-After=%q — %s", method, rawURL, retryAfter, b)
}

// ThrottleBackoff records one 429 and sleeps the computed backoff (Retry-After
// bounded by the front-loaded exponential schedule — see throttleDelay). Shared
// by this package's retry loops and exported for other Fabric-surface clients
// (e.g. the OneLake Table API client) so every throttle feeds the same
// counters, first-429 detail, and live snapshot the spinners render.
func ThrottleBackoff(method, rawURL, retryAfter string, attempt int, body []byte) {
	recordThrottle(method, rawURL, retryAfter, body)
	atomic.AddInt64(&throttleHits, 1)
	atomic.AddInt64(&throttleActive, 1)
	d := throttleDelay(retryAfter, attempt)
	noteThrottle(d, attempt)
	time.Sleep(d)
	if atomic.AddInt64(&throttleActive, -1) == 0 {
		clearThrottleState()
	}
}

// throttleDelay computes how long to wait before retrying a 429.
// Formula: min(retryAfterSecs, 10·2^attempt), clamped to maxThrottleWait (60s).
// This matches fabric-cicd's approach: front-load short waits so we never sit
// idle at 60s on the first 429, while still respecting Retry-After when the
// server sends a shorter deadline. If Retry-After is absent, invalid, or ≤0,
// it defaults to 60s (the hard ceiling), so backoff drives the wait.
//
// Example delays (maxThrottleWait=60s):
//
//	No header:       attempt 0→10s, 1→20s, 2→40s, 3→60s, 4→60s
//	Retry-After=5:   always 5s (5 < 10·2^attempt for any attempt ≥ 0)
//	Retry-After=120: 10s, 20s, 40s, 60s (80 clamped), 60s
func throttleDelay(retryAfter string, attempt int) time.Duration {
	raDefault := int(maxThrottleWait.Seconds()) // 60
	raSecs := raDefault
	if secs, err := strconv.Atoi(strings.TrimSpace(retryAfter)); err == nil && secs > 0 {
		raSecs = secs
	}
	backoffSecs := 10 << uint(attempt) // 10·2^attempt: 10, 20, 40, 80, …
	delay := raSecs
	if backoffSecs < delay {
		delay = backoffSecs
	}
	d := time.Duration(delay) * time.Second
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
//
// A nil (or zero-part) def omits the definition field entirely, creating a
// shell item — required for definitionless types like Warehouse, where the
// API rejects an empty parts collection with 400 InvalidInput.
func CreateItem(token, workspaceID, displayName, itemType string, def *Definition) (Item, error) {
	if err := validateUUID(workspaceID, "workspace ID"); err != nil {
		return Item{}, err
	}
	if def != nil && len(def.Parts) == 0 {
		def = nil
	}
	body := struct {
		DisplayName string      `json:"displayName"`
		Type        string      `json:"type"`
		Definition  *Definition `json:"definition,omitempty"`
	}{
		DisplayName: displayName,
		Type:        itemType,
		Definition:  def,
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

// UpdateItem updates an item's metadata — displayName and description — via the
// Core Items PATCH endpoint. The .platform metadata (which holds the description)
// is never part of the published definition, so this is how a git description
// reaches the workspace; mirrors fabric-cicd's separate metadata update. Unlike
// the definition endpoints this is synchronous (200 OK), not an LRO.
func UpdateItem(token, workspaceID, itemID, displayName, description string) error {
	if err := validateUUID(workspaceID, "workspace ID"); err != nil {
		return err
	}
	if err := validateUUID(itemID, "item ID"); err != nil {
		return err
	}
	body := struct {
		DisplayName string `json:"displayName"`
		Description string `json:"description"`
	}{DisplayName: displayName, Description: description}
	payload, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("marshal update-item body: %w", err)
	}

	url := fmt.Sprintf("%s/v1/workspaces/%s/items/%s", baseURL, workspaceID, itemID)
	resp, respBody, err := doPatch(token, url, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	if resp.StatusCode >= 400 {
		return fmt.Errorf("update item %d: %s", resp.StatusCode, string(respBody))
	}
	return nil
}

// DeleteItem removes an item from a workspace via the Core Items DELETE endpoint.
// Synchronous (200 OK), like UpdateItem — not an LRO. Irreversible; callers must
// confirm before invoking.
func DeleteItem(token, workspaceID, itemID string) error {
	if err := validateUUID(workspaceID, "workspace ID"); err != nil {
		return err
	}
	if err := validateUUID(itemID, "item ID"); err != nil {
		return err
	}
	url := fmt.Sprintf("%s/v1/workspaces/%s/items/%s", baseURL, workspaceID, itemID)
	resp, respBody, err := doDelete(token, url)
	if err != nil {
		return err
	}
	if resp.StatusCode == http.StatusNotFound {
		return nil // already gone — deletion is idempotent, the desired end-state holds
	}
	if resp.StatusCode >= 400 {
		return fmt.Errorf("delete item %d: %s", resp.StatusCode, string(respBody))
	}
	return nil
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
// Format disambiguates definition variants for the types that have several
// (Notebook "ipynb" vs. the default .py form, SparkJobDefinition
// "SparkJobDefinitionV2"); empty means the type's default format.
type Definition struct {
	Format string           `json:"format,omitempty"`
	Parts  []DefinitionPart `json:"parts"`
}

// DefinitionPart is one file inside an item definition. PayloadType
// is almost always "InlineBase64" in practice but Fabric reserves
// other values, so callers should not assume.
type DefinitionPart struct {
	Path        string `json:"path"`
	Payload     string `json:"payload"`
	PayloadType string `json:"payloadType"`
}

// BulkImportOptions configures a bulkImportDefinitions request. AllowPairingByName
// controls how an imported item WITHOUT a logicalId is matched against an existing
// workspace item: true → pair by display name + type; false → name pairing is
// disabled (a same-name/type item causes a duplication conflict). futils strips
// logicalId from the .platform payload (see deploy.stripLogicalID) and sets this
// true, so bulk pairs by name+type — matching the per-item backend's identity model.
type BulkImportOptions struct {
	AllowPairingByName bool `json:"allowPairingByName"`
}

// BulkImportDetail is one item's outcome in a bulk import result.
type BulkImportDetail struct {
	ItemID          string `json:"itemId"`
	ItemDisplayName string `json:"itemDisplayName"`
	ItemType        string `json:"itemType"`
	ItemLogicalID   string `json:"itemLogicalId"`
	OperationType   string `json:"operationType"`   // Create | Update
	OperationStatus string `json:"operationStatus"` // Succeeded | Failed | SucceededDespiteFailures
}

// BulkImportResult is the bulkImportDefinitions response payload.
type BulkImportResult struct {
	Details []BulkImportDetail `json:"importItemDefinitionsDetails"`
}

// BulkImportDefinitions imports many item definitions into a workspace in one
// request via the beta Bulk Import Item Definitions API. parts is the flat list
// of ALL definition parts for ALL items (paths are workspace-absolute and
// item-folder-scoped, e.g. "/Sales.Report/.platform"); Fabric resolves item
// grouping and dependency order itself. The call is an LRO: doLRO returns the
// result body on a synchronous 200 or after polling a 202 to completion, and
// already applies the shared 429-throttle and 401-refresh handling.
//
// The ?beta=true query parameter is REQUIRED while the API is in beta; drop it
// at GA.
func BulkImportDefinitions(token, workspaceID string, parts []DefinitionPart, opts BulkImportOptions) (*BulkImportResult, error) {
	if err := validateUUID(workspaceID, "workspace ID"); err != nil {
		return nil, err
	}
	body := struct {
		DefinitionParts []DefinitionPart  `json:"definitionParts"`
		Options         BulkImportOptions `json:"options"`
	}{DefinitionParts: parts, Options: opts}
	payload, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("marshal bulk import body: %w", err)
	}

	url := fmt.Sprintf("%s/v1/workspaces/%s/items/bulkImportDefinitions?beta=true", baseURL, workspaceID)
	resultBody, err := doLRO(token, url, bytes.NewReader(payload), 150)
	if err != nil {
		return nil, err
	}
	if len(resultBody) == 0 {
		return nil, fmt.Errorf("bulk import returned empty body - operation may have completed but per-item results are unknown; check workspace %s manually", workspaceID)
	}

	var out BulkImportResult
	if err := json.Unmarshal(resultBody, &out); err != nil {
		return nil, fmt.Errorf("parse bulk import result: %w", err)
	}
	return &out, nil
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

const (
	// minPollInterval matches the reference dry-run's steady 1s poll. Polling
	// faster (we tried 250ms) front-loads operation-status requests and spikes
	// the request rate past Fabric's limit, triggering 429s the 1s cadence avoids.
	minPollInterval = 1 * time.Second
	maxPollInterval = 2 * time.Second
)

// nextPollInterval grows the inter-poll delay from minPollInterval toward
// maxPollInterval, so polling starts gentle (matching a known-safe cadence) and
// a slow operation settles to the 2s cap — preserving the ~maxAttempts×2s budget.
func nextPollInterval(prev time.Duration) time.Duration {
	if prev <= 0 {
		return minPollInterval
	}
	next := prev * 2
	if next > maxPollInterval {
		return maxPollInterval
	}
	return next
}

// pollOperation follows a Fabric long-running operation to
// completion and returns the result body. maxAttempts caps the
// number of poll cycles. Pass 0 to use the default of 60 — enough for
// getDefinition; CreateItem uses a longer cap for large report definitions.
//
// It sleeps BEFORE the first poll (never polls at t=0): a freshly-created
// operation isn't ready yet, and polling it immediately — across many
// concurrent workers — makes the upstream service reject the premature polls
// with "RequestBlocked". Waiting one interval first (matching the reference
// dry-run's `sleep(1); poll`) gives the operation time to be ready.
// errOperationHasNoResult is the Fabric error code returned by
// GET /operations/{id}/result for a succeeded LRO that produces no result
// (e.g. updateDefinition). Per the LRO contract this means success, not failure.
const errOperationHasNoResult = "OperationHasNoResult"

func pollOperation(token, operationURL string, maxAttempts int) ([]byte, error) {
	if maxAttempts <= 0 {
		maxAttempts = 60
	}
	var interval time.Duration

	for attempt := 0; attempt < maxAttempts; attempt++ {
		interval = nextPollInterval(interval)
		time.Sleep(interval)

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
			result, err := doGet(token, operationURL+"/result")
			if err != nil && strings.Contains(err.Error(), errOperationHasNoResult) {
				// Per the Fabric LRO contract, "not all long running operations
				// have a result" — updateDefinition succeeds but its /result
				// endpoint returns 400 OperationHasNoResult. That's success with
				// no body, not a failure. Callers that need a body (CreateItem,
				// GetItemDefinition) target operations that DO produce one and so
				// never reach this branch.
				return nil, nil
			}
			return result, err
		case "Failed":
			return nil, fmt.Errorf("operation failed: %s", string(data))
		}
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
			// GET is idempotent, so a transport-level failure (connection reset,
			// EOF, timeout, refused) is safe to retry. http.Client.Do always wraps
			// such errors in *url.Error; request-construction errors are excluded by
			// doGetOnce returning before the call. Bounded retry with backoff.
			var uerr *neturl.Error
			if errors.As(err, &uerr) && attempt < maxNetRetries {
				sleep(netBackoff(attempt))
				continue
			}
			return nil, err
		}
		// On 401, the token captured at flow start has likely expired during
		// a long-running poll. Mint a fresh one via cached/refresh-grant
		// (browser auth is NOT triggered here — that requires interactive
		// terminal context) and retry once.
		if status == http.StatusUnauthorized {
			if fresh, ok := retryTokenFn(); ok {
				token = fresh
				body, status, retryAfter, err = doGetOnce(token, rawURL)
				if err != nil {
					return nil, err
				}
			}
		}
		if status == http.StatusTooManyRequests && attempt < maxThrottleRetries {
			ThrottleBackoff("GET", rawURL, retryAfter, attempt, body)
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
	start := time.Now()
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, 0, "", err
	}
	defer resp.Body.Close()
	logHTTP("GET", resp.StatusCode, time.Since(start), rawURL)
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
	return doWrite("POST", token, rawURL, reqBody)
}

func doPatch(token, rawURL string, reqBody io.Reader) (*http.Response, []byte, error) {
	return doWrite("PATCH", token, rawURL, reqBody)
}

func doDelete(token, rawURL string) (*http.Response, []byte, error) {
	return doWrite("DELETE", token, rawURL, nil)
}

// doWrite issues a body-bearing request (POST/PATCH) with the shared 401-refresh
// and 429-throttle retry handling.
func doWrite(method, token, rawURL string, reqBody io.Reader) (*http.Response, []byte, error) {
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
		resp, body, err := doWriteOnce(method, token, rawURL, bodyBytes)
		if err != nil {
			return resp, body, err
		}
		if resp.StatusCode == http.StatusUnauthorized {
			if fresh, ok := retryTokenFn(); ok {
				token = fresh
				resp, body, err = doWriteOnce(method, token, rawURL, bodyBytes)
				if err != nil {
					return resp, body, err
				}
			}
		}
		if resp.StatusCode == http.StatusTooManyRequests && attempt < maxThrottleRetries {
			ThrottleBackoff(method, rawURL, resp.Header.Get("Retry-After"), attempt, body)
			continue
		}
		return resp, body, nil
	}
}

func doWriteOnce(method, token, rawURL string, bodyBytes []byte) (*http.Response, []byte, error) {
	var reader io.Reader
	if bodyBytes != nil {
		reader = bytes.NewReader(bodyBytes)
	}
	req, err := http.NewRequest(method, rawURL, reader)
	if err != nil {
		return nil, nil, fmt.Errorf("invalid URL: %w", err)
	}
	req.Header = authHeader(token)
	if bodyBytes != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	start := time.Now()
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, nil, err
	}
	defer resp.Body.Close()
	logHTTP(method, resp.StatusCode, time.Since(start), rawURL)
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
	// Must be the lakehouse-specific endpoint: the generic /items/{id} GET
	// returns only the common item envelope, never the type-specific
	// properties block that carries sqlEndpointProperties.
	url := fmt.Sprintf("%s/v1/workspaces/%s/lakehouses/%s", baseURL, workspaceID, lakehouseID)
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
