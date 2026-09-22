package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"strings"
	"time"
)

// MCP client: minimal MCP JSON-RPC over streamable HTTP (mcp_client.py
// port). Handshake: initialize -> capture mcp-session-id response header
// (case-insensitive) -> notifications/initialized. tools/call sends the
// session header; a stale/lost session re-handshakes once and retries.
// Response bodies arrive in three shapes: SSE (data: {...} lines), plain
// JSON, or empty — handle all three. result.content[0].text is itself a
// JSON string: parse twice, fall back to the raw string.

const (
	protocolVersion = "2024-11-05"
	userAgent       = "lybrix-eval/0.1.0"
	requestTimeout  = 60 * time.Second
)

// McpConfigError is raised when LYBRIX_MCP_URL / LYBRIX_MCP_TOKEN is unset.
type McpConfigError struct{ Var string }

func (e *McpConfigError) Error() string { return e.Var + " is not set" }

// McpRPCError is raised when the server returns a JSON-RPC error with no
// recovery path.
type McpRPCError struct{ Detail string }

func (e *McpRPCError) Error() string { return "mcp: " + e.Detail }

// PostFn is the transport seam: (url, headers, body) -> (status, headers, body).
type PostFn func(url string, headers map[string]string, body []byte) (int, map[string]string, []byte)

// McpClient is a self-contained MCP client; pass postFn to run fully
// offline in tests.
type McpClient struct {
	postFn    PostFn
	url       string
	token     string
	SessionID string
}

// NewMcpClient reads config from env only.
func NewMcpClient() (*McpClient, error) {
	url := os.Getenv("LYBRIX_MCP_URL")
	token := os.Getenv("LYBRIX_MCP_TOKEN")
	if url == "" {
		return nil, &McpConfigError{"LYBRIX_MCP_URL"}
	}
	if token == "" {
		return nil, &McpConfigError{"LYBRIX_MCP_TOKEN"}
	}
	return &McpClient{
		postFn: defaultPostFn,
		url:    url,
		token:  token,
	}, nil
}

func defaultPostFn(url string, headers map[string]string, body []byte) (int, map[string]string, []byte) {
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return 0, nil, nil
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	client := &http.Client{Timeout: requestTimeout}
	resp, err := client.Do(req)
	if err != nil {
		return 0, nil, nil
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	hdrs := map[string]string{}
	for k, vs := range resp.Header {
		if len(vs) > 0 {
			hdrs[strings.ToLower(k)] = vs[0]
		}
	}
	return resp.StatusCode, hdrs, b
}

var sessionHeaderRE = regexp.MustCompile(`(?i)mcp-session-id:\s*(\S+)`)

func sessionIDFrom(headers map[string]string) string {
	if v, ok := headers["mcp-session-id"]; ok && v != "" {
		return strings.TrimSpace(v)
	}
	// fallback: regex over the raw header text (covers odd fake transports)
	if m := sessionHeaderRE.FindStringSubmatch(fmt.Sprintf("%v", headers)); m != nil {
		return m[1]
	}
	return ""
}

func (c *McpClient) rpc(method string, params map[string]any) (int, map[string]string, []byte) {
	body := map[string]any{"jsonrpc": "2.0", "id": 1, "method": method}
	if params != nil {
		body["params"] = params
	}
	b, _ := json.Marshal(body)
	headers := map[string]string{
		"Content-Type":  "application/json",
		"Accept":        "application/json, text/event-stream",
		"Authorization": "Bearer " + c.token,
		"User-Agent":    userAgent,
	}
	if c.SessionID != "" {
		headers["mcp-session-id"] = c.SessionID
	}
	return c.postFn(c.url, headers, b)
}

// ParseBody handles SSE (first data: line), plain JSON, or empty -> nil.
func ParseBody(raw []byte) map[string]any {
	text := string(raw)
	for _, line := range strings.Split(text, "\n") {
		if strings.HasPrefix(line, "data:") {
			var out map[string]any
			if err := json.Unmarshal([]byte(strings.TrimSpace(line[len("data:"):])), &out); err == nil {
				return out
			}
		}
	}
	if strings.TrimSpace(text) != "" {
		var out map[string]any
		if err := json.Unmarshal([]byte(text), &out); err == nil {
			return out
		}
	}
	return nil
}

// ParseResultText parses result.content[0].text (itself a JSON string) —
// fall back to the raw string when it is not JSON.
func ParseResultText(text string) any {
	var out any
	if err := json.Unmarshal([]byte(text), &out); err == nil {
		return out
	}
	return text
}

func (c *McpClient) extractResult(payload map[string]any) (any, error) {
	if payload == nil {
		return nil, nil
	}
	if e, ok := payload["error"]; ok && e != nil {
		return nil, &McpRPCError{Detail: fmt.Sprintf("%v", e)}
	}
	result, _ := payload["result"].(map[string]any)
	content, _ := result["content"].([]any)
	if len(content) == 0 {
		return nil, nil
	}
	first, _ := content[0].(map[string]any)
	text, _ := first["text"].(string)
	return ParseResultText(text), nil
}

// Handshake: initialize -> capture session id -> notifications/initialized.
func (c *McpClient) Handshake() error {
	_, headers, body := c.rpc("initialize", map[string]any{
		"protocolVersion": protocolVersion,
		"capabilities":    map[string]any{},
		"clientInfo":      map[string]any{"name": "lybrix-eval", "version": "0.1.0"},
	})
	payload := ParseBody(body)
	if payload != nil {
		if e, ok := payload["error"]; ok && e != nil {
			return &McpRPCError{Detail: "initialize failed: " + fmt.Sprintf("%v", e)}
		}
	}
	c.SessionID = sessionIDFrom(headers)
	// notifications/initialized: server replies HTTP 202 with no body.
	c.rpc("notifications/initialized", map[string]any{})
	return nil
}

// Call runs tools/call with the session header; a stale/lost session
// re-handshakes once and retries.
func (c *McpClient) Call(name string, args map[string]any, retry bool) (any, error) {
	_, _, body := c.rpc("tools/call", map[string]any{"name": name, "arguments": args})
	payload := ParseBody(body)
	if payload == nil || payload["error"] != nil {
		errDetail := ""
		if payload != nil {
			errDetail = fmt.Sprintf("%v", payload["error"])
		}
		if retry && payload == nil || retry && strings.Contains(strings.ToLower(errDetail), "missing session") {
			if err := c.Handshake(); err != nil {
				return nil, err
			}
			return c.Call(name, args, false)
		}
		if payload == nil && !retry {
			return nil, &McpRPCError{Detail: "tools/call " + name + ": unparseable response"}
		}
		if payload == nil {
			// unparseable/empty body mid-run: retry via fresh handshake
			if err := c.Handshake(); err != nil {
				return nil, err
			}
			return c.Call(name, args, false)
		}
		return nil, &McpRPCError{Detail: "tools/call " + name + " failed: " + errDetail}
	}
	return c.extractResult(payload)
}

// Search returns items with doc_title, page_start/page_end (sometimes null),
// heading_path (list), text, score, doc_id, chunk_id.
func (c *McpClient) Search(query string, collection *string, topK int) ([]map[string]any, error) {
	args := map[string]any{"query": query, "top_k": topK}
	if collection != nil {
		args["collection"] = *collection
	}
	result, err := c.Call("search", args, true)
	if err != nil {
		return nil, err
	}
	rows, ok := result.([]any)
	if !ok {
		return []map[string]any{}, nil
	}
	out := []map[string]any{}
	for _, r := range rows {
		if m, ok := r.(map[string]any); ok {
			out = append(out, m)
		}
	}
	return out, nil
}

// ListCollections returns [{id, name, embedding_model, doc_count}] — rows
// whose `id` is what the search tool's collection arg actually filters on.
func (c *McpClient) ListCollections() ([]map[string]any, error) {
	result, err := c.Call("list_collections", map[string]any{}, true)
	if err != nil {
		return nil, err
	}
	rows, ok := result.([]any)
	if !ok {
		return nil, nil
	}
	out := []map[string]any{}
	for _, r := range rows {
		if m, ok := r.(map[string]any); ok {
			out = append(out, m)
		}
	}
	return out, nil
}
