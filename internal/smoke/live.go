package smoke

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

const (
	liveTimeout     = 10 * time.Second
	mcpProtoVersion = "2024-11-05"
)

// jsonrpcRequest is a JSON-RPC 2.0 request.
type jsonrpcRequest struct {
	JSONRPC string      `json:"jsonrpc"`
	ID      int         `json:"id"`
	Method  string      `json:"method"`
	Params  interface{} `json:"params"`
}

// jsonrpcResponse is a JSON-RPC 2.0 response.
type jsonrpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      int             `json:"id"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *jsonrpcError   `json:"error,omitempty"`
}

type jsonrpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type initializeResult struct {
	ProtocolVersion string     `json:"protocolVersion"`
	ServerInfo      serverInfo `json:"serverInfo"`
}

type serverInfo struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

type toolsListResult struct {
	Tools []toolEntry `json:"tools"`
}

type toolEntry struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}

// LiveCheck starts a stdio MCP server, sends initialize and tools/list,
// and returns findings about what works and what fails.
func LiveCheck(spec serverSpec, baseDir string) []Finding {
	if spec.Command == "" || spec.URL != "" {
		return nil // skip remote servers and empty commands
	}

	serverName := spec.Name
	if serverName == "" {
		serverName = defaultServerLabel
	}

	// Resolve cwd
	cwd := baseDir
	if spec.Cwd != "" {
		cwd = resolvePath(baseDir, spec.Cwd)
	}

	// Build command
	ctx, cancel := context.WithTimeout(context.Background(), liveTimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, spec.Command, spec.Args...)
	cmd.Dir = cwd

	// Set env if specified
	if len(spec.Env) > 0 {
		env := cmd.Environ()
		for k, v := range spec.Env {
			env = append(env, k+"="+v)
		}
		cmd.Env = env
	}

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return []Finding{{
			Server:   serverName,
			Severity: SeverityError,
			Problem:  fmt.Sprintf("live: failed to create stdin pipe: %s", err),
			Fix:      "Check that the command is a valid executable.",
		}}
	}

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return []Finding{{
			Server:   serverName,
			Severity: SeverityError,
			Problem:  fmt.Sprintf("live: failed to create stdout pipe: %s", err),
			Fix:      "Check that the command is a valid executable.",
		}}
	}

	// Start the server process
	if err := cmd.Start(); err != nil {
		return []Finding{{
			Server:   serverName,
			Severity: SeverityError,
			Problem:  fmt.Sprintf("live: process failed to start: %s", err),
			Fix:      "Install the command or fix the executable path.",
		}}
	}

	// Ensure cleanup
	defer func() {
		stdin.Close()
		if cmd.Process != nil {
			cmd.Process.Kill()
		}
		cmd.Wait()
	}()

	var findings []Finding
	reader := bufio.NewReader(stdout)

	// Step 1: Send initialize
	initReq := jsonrpcRequest{
		JSONRPC: "2.0",
		ID:      1,
		Method:  "initialize",
		Params: map[string]interface{}{
			"protocolVersion": mcpProtoVersion,
			"capabilities":   map[string]interface{}{},
			"clientInfo": map[string]string{
				"name":    "mcp-smoke",
				"version": "0.1.0",
			},
		},
	}

	if err := writeMessage(stdin, initReq); err != nil {
		findings = append(findings, Finding{
			Server:   serverName,
			Severity: SeverityError,
			Problem:  fmt.Sprintf("live: failed to send initialize: %s", err),
			Fix:      "The server process started but does not accept input.",
		})
		return findings
	}

	initResp, err := readMessage(reader, ctx)
	if err != nil {
		problem := fmt.Sprintf("live: initialize failed: %s", err)
		if ctx.Err() == context.DeadlineExceeded {
			problem = fmt.Sprintf("live: initialize timed out after %s", liveTimeout)
		}
		findings = append(findings, Finding{
			Server:   serverName,
			Severity: SeverityError,
			Problem:  problem,
			Fix:      "The server did not respond to the initialize request. Check server logs.",
		})
		return findings
	}

	if initResp.Error != nil {
		findings = append(findings, Finding{
			Server:   serverName,
			Severity: SeverityError,
			Problem:  fmt.Sprintf("live: initialize returned error: %s (code %d)", initResp.Error.Message, initResp.Error.Code),
			Fix:      "The server rejected the initialize request. Check protocol version compatibility.",
		})
		return findings
	}

	// Parse initialize result
	var initResult initializeResult
	if err := json.Unmarshal(initResp.Result, &initResult); err == nil {
		detail := fmt.Sprintf("live: server ready — %s %s (protocol %s)",
			initResult.ServerInfo.Name, initResult.ServerInfo.Version, initResult.ProtocolVersion)
		findings = append(findings, Finding{
			Server:   serverName,
			Severity: SeverityInfo,
			Problem:  detail,
			Fix:      "No action needed.",
		})
	}

	// Send initialized notification (required by MCP protocol)
	notif := jsonrpcRequest{
		JSONRPC: "2.0",
		ID:      0,
		Method:  "notifications/initialized",
		Params:  map[string]interface{}{},
	}
	writeMessage(stdin, notif)

	// Step 2: Send tools/list
	toolsReq := jsonrpcRequest{
		JSONRPC: "2.0",
		ID:      2,
		Method:  "tools/list",
		Params:  map[string]interface{}{},
	}

	if err := writeMessage(stdin, toolsReq); err != nil {
		findings = append(findings, Finding{
			Server:   serverName,
			Severity: SeverityWarning,
			Problem:  fmt.Sprintf("live: failed to send tools/list: %s", err),
			Fix:      "The server may have closed the connection after initialize.",
		})
		return findings
	}

	toolsResp, err := readMessage(reader, ctx)
	if err != nil {
		problem := fmt.Sprintf("live: tools/list failed: %s", err)
		if ctx.Err() == context.DeadlineExceeded {
			problem = fmt.Sprintf("live: tools/list timed out after %s", liveTimeout)
		}
		findings = append(findings, Finding{
			Server:   serverName,
			Severity: SeverityWarning,
			Problem:  problem,
			Fix:      "The server did not respond to tools/list. It may not support tool listing.",
		})
		return findings
	}

	if toolsResp.Error != nil {
		findings = append(findings, Finding{
			Server:   serverName,
			Severity: SeverityWarning,
			Problem:  fmt.Sprintf("live: tools/list returned error: %s", toolsResp.Error.Message),
			Fix:      "The server does not support tools/list or returned an error.",
		})
		return findings
	}

	// Parse tools list
	var toolsResult toolsListResult
	if err := json.Unmarshal(toolsResp.Result, &toolsResult); err == nil {
		detail := fmt.Sprintf("live: %d tool(s) available", len(toolsResult.Tools))
		if len(toolsResult.Tools) > 0 && len(toolsResult.Tools) <= 5 {
			var names []string
			for _, t := range toolsResult.Tools {
				names = append(names, t.Name)
			}
			detail += " — " + strings.Join(names, ", ")
		}
		findings = append(findings, Finding{
			Server:   serverName,
			Severity: SeverityInfo,
			Problem:  detail,
			Fix:      "No action needed.",
		})
	}

	return findings
}

// writeMessage sends a JSON-RPC message with Content-Length header (MCP stdio protocol).
func writeMessage(w io.Writer, msg jsonrpcRequest) error {
	body, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("marshal failed: %w", err)
	}

	header := fmt.Sprintf("Content-Length: %d\r\n\r\n", len(body))
	if _, err := io.WriteString(w, header); err != nil {
		return fmt.Errorf("write header failed: %w", err)
	}
	if _, err := w.Write(body); err != nil {
		return fmt.Errorf("write body failed: %w", err)
	}
	return nil
}

// readMessage reads a JSON-RPC response with Content-Length header.
func readMessage(r *bufio.Reader, ctx context.Context) (*jsonrpcResponse, error) {
	type result struct {
		resp *jsonrpcResponse
		err  error
	}

	ch := make(chan result, 1)
	go func() {
		resp, err := readMessageBlocking(r)
		ch <- result{resp, err}
	}()

	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case res := <-ch:
		return res.resp, res.err
	}
}

func readMessageBlocking(r *bufio.Reader) (*jsonrpcResponse, error) {
	// Read headers until empty line
	contentLength := 0
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return nil, fmt.Errorf("read header failed: %w", err)
		}
		line = strings.TrimSpace(line)
		if line == "" {
			break
		}
		if strings.HasPrefix(line, "Content-Length:") {
			val := strings.TrimSpace(strings.TrimPrefix(line, "Content-Length:"))
			n, err := strconv.Atoi(val)
			if err != nil {
				return nil, fmt.Errorf("invalid Content-Length: %s", val)
			}
			contentLength = n
		}
	}

	if contentLength == 0 {
		return nil, fmt.Errorf("missing Content-Length header")
	}

	// Read body
	body := make([]byte, contentLength)
	if _, err := io.ReadFull(r, body); err != nil {
		return nil, fmt.Errorf("read body failed: %w", err)
	}

	var resp jsonrpcResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("parse response failed: %w", err)
	}

	return &resp, nil
}
