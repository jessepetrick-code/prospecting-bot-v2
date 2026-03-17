package tools

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"time"
)

// mcpEndpoint is the Common Room MCP server endpoint.
const mcpEndpoint = "https://mcp.commonroom.io/mcp/"

// crMCPClient is a lightweight MCP Streamable-HTTP client for Common Room.
type crMCPClient struct {
	apiKey    string
	sessionID string
	idCounter atomic.Int64
}

// jsonRPCRequest is a JSON-RPC 2.0 request.
type jsonRPCRequest struct {
	JSONRPC string `json:"jsonrpc"`
	ID      int64  `json:"id"`
	Method  string `json:"method"`
	Params  any    `json:"params,omitempty"`
}

// jsonRPCResponse is a JSON-RPC 2.0 response.
type jsonRPCResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      int64           `json:"id"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error,omitempty"`
}

// mcpToolResult is the result returned by tools/call.
type mcpToolResult struct {
	Content []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
	IsError bool `json:"isError"`
}

// newCRMCPClient creates a client and runs the initialize handshake.
// Returns an error if the API key is empty or auth fails.
func newCRMCPClient(ctx context.Context, apiKey string) (*crMCPClient, error) {
	if apiKey == "" {
		return nil, fmt.Errorf("COMMONROOM_API_KEY not set")
	}
	c := &crMCPClient{apiKey: apiKey}
	if err := c.initialize(ctx); err != nil {
		return nil, err
	}
	return c, nil
}

// initialize sends the MCP initialize handshake and captures the session ID.
func (c *crMCPClient) initialize(ctx context.Context) error {
	params := map[string]any{
		"protocolVersion": "2024-11-05",
		"capabilities":    map[string]any{},
		"clientInfo": map[string]any{
			"name":    "c1-prospecting-bot",
			"version": "2.0",
		},
	}
	_, respHeaders, err := c.send(ctx, "initialize", params)
	if err != nil {
		return fmt.Errorf("MCP initialize: %w", err)
	}
	if sid := respHeaders.Get("Mcp-Session-Id"); sid != "" {
		c.sessionID = sid
	}
	return nil
}

// CallTool invokes an MCP tool and returns the text content of the result.
func (c *crMCPClient) CallTool(ctx context.Context, toolName string, arguments any) (string, error) {
	params := map[string]any{
		"name":      toolName,
		"arguments": arguments,
	}
	result, _, err := c.send(ctx, "tools/call", params)
	if err != nil {
		return "", err
	}

	var toolResult mcpToolResult
	if err := json.Unmarshal(result, &toolResult); err != nil {
		return "", fmt.Errorf("failed to parse tool result: %w", err)
	}
	if toolResult.IsError {
		texts := make([]string, 0, len(toolResult.Content))
		for _, c := range toolResult.Content {
			texts = append(texts, c.Text)
		}
		return "", fmt.Errorf("MCP tool error: %s", strings.Join(texts, "; "))
	}

	var parts []string
	for _, block := range toolResult.Content {
		if block.Type == "text" {
			parts = append(parts, block.Text)
		}
	}
	return strings.Join(parts, "\n"), nil
}

// send posts a JSON-RPC request and reads the response (JSON or SSE).
func (c *crMCPClient) send(ctx context.Context, method string, params any) (json.RawMessage, http.Header, error) {
	id := c.idCounter.Add(1)
	req := jsonRPCRequest{
		JSONRPC: "2.0",
		ID:      id,
		Method:  method,
		Params:  params,
	}

	body, err := json.Marshal(req)
	if err != nil {
		return nil, nil, err
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, mcpEndpoint, bytes.NewReader(body))
	if err != nil {
		return nil, nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "application/json, text/event-stream")
	httpReq.Header.Set("Authorization", "Bearer "+c.apiKey)
	if c.sessionID != "" {
		httpReq.Header.Set("Mcp-Session-Id", c.sessionID)
	}

	client := &http.Client{Timeout: 60 * time.Second}
	resp, err := client.Do(httpReq)
	if err != nil {
		return nil, nil, fmt.Errorf("HTTP error: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusUnauthorized {
		return nil, nil, fmt.Errorf("Common Room MCP auth failed (401) — check COMMONROOM_API_KEY or try REST API")
	}
	if resp.StatusCode == http.StatusForbidden {
		return nil, nil, fmt.Errorf("Common Room MCP access denied (403)")
	}
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusAccepted {
		bodyBytes, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, nil, fmt.Errorf("Common Room MCP error %d: %s", resp.StatusCode, string(bodyBytes))
	}

	ct := resp.Header.Get("Content-Type")
	if strings.Contains(ct, "text/event-stream") {
		result, err := readSSEResponse(resp.Body, id)
		return result, resp.Header, err
	}

	// Plain JSON response
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, nil, err
	}
	var rpcResp jsonRPCResponse
	if err := json.Unmarshal(respBody, &rpcResp); err != nil {
		return nil, nil, fmt.Errorf("failed to parse JSON-RPC response: %w", err)
	}
	if rpcResp.Error != nil {
		return nil, nil, fmt.Errorf("JSON-RPC error %d: %s", rpcResp.Error.Code, rpcResp.Error.Message)
	}
	return rpcResp.Result, resp.Header, nil
}

// readSSEResponse reads an SSE stream and extracts the JSON-RPC response with the matching ID.
func readSSEResponse(r io.Reader, targetID int64) (json.RawMessage, error) {
	scanner := bufio.NewScanner(r)
	var dataLines []string

	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "data:") {
			data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
			if data != "" {
				dataLines = append(dataLines, data)
			}
		} else if line == "" && len(dataLines) > 0 {
			// End of SSE event — try to parse
			combined := strings.Join(dataLines, "")
			dataLines = nil

			var rpcResp jsonRPCResponse
			if err := json.Unmarshal([]byte(combined), &rpcResp); err != nil {
				continue
			}
			if rpcResp.ID != targetID {
				continue
			}
			if rpcResp.Error != nil {
				return nil, fmt.Errorf("JSON-RPC error %d: %s", rpcResp.Error.Code, rpcResp.Error.Message)
			}
			return rpcResp.Result, nil
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("SSE read error: %w", err)
	}
	return nil, fmt.Errorf("SSE stream ended without a matching response for id=%d", targetID)
}

// ── Territory → Location ID mapping ────────────────────────────────────────
// Common Room uses loc_XXXXXX IDs for geographic filtering.
// These are the US state location IDs used by Common Room's object API.
var stateLocationIDs = map[string]string{
	// Northeast
	"ME": "loc_200387",
	"VT": "loc_200453",
	"NH": "loc_200408",
	"NY": "loc_200405",
	"MA": "loc_200386",
	"CT": "loc_200362",
	"RI": "loc_200436",
	"PA": "loc_200428",
	"NJ": "loc_200411",
	"DE": "loc_200363",
	"MD": "loc_200385",
	"DC": "loc_200364",
	"VA": "loc_200454",
	"NC": "loc_200413",
	"SC": "loc_200440",
	"GA": "loc_200370",
	"FL": "loc_200367",
	// South
	"MS": "loc_200396",
	"TN": "loc_200447",
	"AL": "loc_200349",
	"WV": "loc_200460",
	"KY": "loc_200382",
	"OH": "loc_200419",
	"IN": "loc_200378",
	"MI": "loc_200392",
	"IL": "loc_200376",
	"WI": "loc_200461",
	"MN": "loc_200394",
	"IA": "loc_200379",
	"ND": "loc_200416",
	"SD": "loc_200443",
	// Midwest / Plains
	"MO": "loc_200397",
	"AR": "loc_200354",
	"LA": "loc_200383",
	"TX": "loc_200449",
	"OK": "loc_200421",
	"KS": "loc_200381",
	"NE": "loc_200406",
	// West
	"CA": "loc_200358",
	"OR": "loc_200424",
	"WA": "loc_200457",
	"ID": "loc_200375",
	"MT": "loc_200399",
	"WY": "loc_200463",
	"AK": "loc_200348",
	"HI": "loc_200374",
	"NV": "loc_200407",
	"AZ": "loc_200355",
	"NM": "loc_200412",
	"UT": "loc_200452",
	"CO": "loc_200361",
}

// statesToLocationIDs converts a list of state abbreviations to Common Room location IDs.
// Unknown states are skipped with a logged warning.
func statesToLocationIDs(states []string) []string {
	ids := make([]string, 0, len(states))
	for _, s := range states {
		if id, ok := stateLocationIDs[strings.ToUpper(s)]; ok {
			ids = append(ids, id)
		}
	}
	return ids
}
