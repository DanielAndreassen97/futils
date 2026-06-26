package schemacompare

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"time"
)

const defaultBaseURL = "https://onelake.table.fabric.microsoft.com/delta"

// OneLakeTableAPI is the read-only metadata surface schemacompare needs.
type OneLakeTableAPI interface {
	ListSchemas(wsID, lhID string) ([]string, error)
	ListTables(wsID, lhID, schema string) ([]string, error)
	GetTable(wsID, lhID, schema, table string) ([]ColumnSchema, error)
}

// Client talks to the OneLake Table API (Unity Catalog / Delta protocol).
type Client struct {
	token   string
	baseURL string
	http    *http.Client
}

func NewClient(token string) *Client { return NewClientWithBase(token, defaultBaseURL) }

func NewClientWithBase(token, baseURL string) *Client {
	// Tune the transport for the burst of concurrent GetTable calls a compare
	// makes against a single host. Go's default MaxIdleConnsPerHost is 2, so
	// without this only 2 of N concurrent connections get reused and the rest
	// pay a fresh TLS handshake every call. Sizing the idle pool to the fetch
	// concurrency keeps connections warm across the burst.
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.MaxIdleConns = 64
	tr.MaxIdleConnsPerHost = 32
	tr.MaxConnsPerHost = 32
	tr.IdleConnTimeout = 90 * time.Second
	return &Client{token: token, baseURL: baseURL, http: &http.Client{Timeout: 60 * time.Second, Transport: tr}}
}

// catalog is the Unity-Catalog catalog token for a lakehouse item.
func catalog(lhID string) string { return lhID }

func (c *Client) base(wsID, lhID string) string {
	return fmt.Sprintf("%s/%s/%s/api/2.1/unity-catalog", c.baseURL, wsID, lhID)
}

// get issues a GET with the bearer token, retrying transient 429s a few times,
// and decodes the JSON body into out.
func (c *Client) get(rawURL string, out any) error {
	for attempt := 0; ; attempt++ {
		req, err := http.NewRequest(http.MethodGet, rawURL, nil)
		if err != nil {
			return err
		}
		req.Header.Set("Authorization", "Bearer "+c.token)
		req.Header.Set("Content-Type", "application/json")
		resp, err := c.http.Do(req)
		if err != nil {
			return err
		}
		body, rerr := readAllClose(resp)
		if rerr != nil {
			return fmt.Errorf("read response from %s: %w", rawURL, rerr)
		}
		if resp.StatusCode == http.StatusTooManyRequests && attempt < 5 {
			time.Sleep(time.Duration(attempt+1) * 2 * time.Second)
			continue
		}
		if resp.StatusCode >= 400 {
			return fmt.Errorf("GET %s → %d: %s", rawURL, resp.StatusCode, string(body))
		}
		return json.Unmarshal(body, out)
	}
}

func (c *Client) ListSchemas(wsID, lhID string) ([]string, error) {
	var names []string
	pageToken := ""
	for {
		u := fmt.Sprintf("%s/schemas?catalog_name=%s", c.base(wsID, lhID), url.QueryEscape(catalog(lhID)))
		if pageToken != "" {
			u += "&page_token=" + url.QueryEscape(pageToken)
		}
		var page struct {
			Schemas []struct {
				Name string `json:"name"`
			} `json:"schemas"`
			NextPageToken string `json:"next_page_token"`
		}
		if err := c.get(u, &page); err != nil {
			return nil, err
		}
		for _, s := range page.Schemas {
			names = append(names, s.Name)
		}
		if page.NextPageToken == "" {
			break
		}
		pageToken = page.NextPageToken
	}
	sort.Strings(names)
	return names, nil
}

func (c *Client) ListTables(wsID, lhID, schema string) ([]string, error) {
	var names []string
	pageToken := ""
	for {
		u := fmt.Sprintf("%s/tables?catalog_name=%s&schema_name=%s",
			c.base(wsID, lhID), url.QueryEscape(catalog(lhID)), url.QueryEscape(schema))
		if pageToken != "" {
			u += "&page_token=" + url.QueryEscape(pageToken)
		}
		var page struct {
			Tables []struct {
				Name string `json:"name"`
			} `json:"tables"`
			NextPageToken string `json:"next_page_token"`
		}
		if err := c.get(u, &page); err != nil {
			return nil, err
		}
		for _, tb := range page.Tables {
			names = append(names, tb.Name)
		}
		if page.NextPageToken == "" {
			break
		}
		pageToken = page.NextPageToken
	}
	sort.Strings(names)
	return names, nil
}

func (c *Client) GetTable(wsID, lhID, schema, table string) ([]ColumnSchema, error) {
	full := fmt.Sprintf("%s.%s.%s", catalog(lhID), schema, table)
	u := fmt.Sprintf("%s/tables/%s", c.base(wsID, lhID), url.PathEscape(full))
	var resp struct {
		Columns []struct {
			Name          string `json:"name"`
			TypeName      string `json:"type_name"`
			TypePrecision int    `json:"type_precision"`
			TypeScale     int    `json:"type_scale"`
			Nullable      bool   `json:"nullable"`
			Position      int    `json:"position"`
		} `json:"columns"`
	}
	if err := c.get(u, &resp); err != nil {
		return nil, err
	}
	cols := make([]ColumnSchema, 0, len(resp.Columns))
	for _, col := range resp.Columns {
		cols = append(cols, ColumnSchema{
			Name:     col.Name,
			Type:     formatType(col.TypeName, col.TypePrecision, col.TypeScale),
			Nullable: col.Nullable,
			Position: col.Position,
		})
	}
	return cols, nil
}

func readAllClose(resp *http.Response) ([]byte, error) {
	defer resp.Body.Close()
	return io.ReadAll(resp.Body)
}
