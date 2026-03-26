package smoke

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

const (
	// ExitCodeSuccess indicates the smoke check completed without findings.
	ExitCodeSuccess = 0
	// ExitCodeUserError indicates the input or config needs attention.
	ExitCodeUserError = 1
	// ExitCodeSystemError indicates an unexpected tool failure.
	ExitCodeSystemError = 2

	// KindDesktopConfig identifies the mcpServers config shape.
	KindDesktopConfig = "mcpServers"
	// KindServerList identifies the servers array config shape.
	KindServerList = "servers"
	// KindSingleServer identifies a single server object.
	KindSingleServer = "single-server"
	// SeverityError marks a blocking finding.
	SeverityError = "error"
	// SeverityWarning marks a non-blocking but actionable finding.
	SeverityWarning = "warning"
	// SeverityInfo marks an informational finding.
	SeverityInfo       = "info"
	defaultServerLabel = "unnamed"
)

// ErrorKind classifies a failure so the CLI can return the right exit code.
type ErrorKind string

const (
	// ErrorKindUser marks an error caused by invalid input or config.
	ErrorKindUser ErrorKind = "user"
	// ErrorKindSystem marks an unexpected system failure.
	ErrorKindSystem ErrorKind = "system"
)

// AppError wraps a failure with an exit-code classification.
type AppError struct {
	Kind ErrorKind
	Err  error
}

// Error returns the wrapped error message.
func (e *AppError) Error() string {
	if e == nil {
		return ""
	}
	if e.Err != nil {
		return e.Err.Error()
	}
	return string(e.Kind)
}

// Unwrap returns the underlying error.
func (e *AppError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

// Options controls which smoke checks are enabled.
type Options struct {
	SkipCwd  bool
	SkipEnv  bool
	SkipPath bool
}

// Finding describes one actionable issue discovered during analysis.
type Finding struct {
	Server   string `json:"server"`
	Severity string `json:"severity"`
	Problem  string `json:"problem"`
	Fix      string `json:"fix"`
}

// Report contains the normalized config summary and all findings.
type Report struct {
	ConfigPath  string    `json:"configPath"`
	ConfigKind  string    `json:"configKind"`
	ServerCount int       `json:"serverCount"`
	Findings    []Finding `json:"findings"`
}

type serverSpec struct {
	Name      string            `json:"name"`
	Command   string            `json:"command"`
	Args      []string          `json:"args"`
	Cwd       string            `json:"cwd"`
	Env       map[string]string `json:"env"`
	Transport string            `json:"transport"`
	URL       string            `json:"url"`
}

type configProbe struct {
	MCPServers map[string]serverSpec `json:"mcpServers"`
	Servers    []serverSpec          `json:"servers"`
	Command    json.RawMessage       `json:"command"`
	URL        json.RawMessage       `json:"url"`
}

type quickServerSpec struct {
	Command   string `json:"command"`
	Transport string `json:"transport"`
	URL       string `json:"url"`
}

type quickDesktopConfig struct {
	MCPServers map[string]quickServerSpec `json:"mcpServers"`
}

// AnalyzeFile reads a config file, normalizes supported MCP formats, and returns a report.
func AnalyzeFile(configPath string, options Options) (Report, error) {
	content, err := os.ReadFile(configPath)
	if err != nil {
		return Report{}, &AppError{
			Kind: ErrorKindUser,
			Err:  fmt.Errorf("read config file failed: %w", err),
		}
	}

	if options.SkipCwd && options.SkipEnv && options.SkipPath {
		if report, ok := analyzeFastDesktop(configPath, content); ok {
			return report, nil
		}
	}

	configKind, servers, err := parseServers(content)
	if err != nil {
		return Report{}, &AppError{
			Kind: ErrorKindUser,
			Err:  fmt.Errorf("parse config file failed: %w", err),
		}
	}

	baseDir := filepath.Dir(configPath)
	findings := make([]Finding, 0, len(servers))
	for _, server := range servers {
		findings = append(findings, analyzeServer(baseDir, server, options)...)
	}

	if len(findings) > 1 {
		sortFindings(findings)
	}

	return Report{
		ConfigPath:  configPath,
		ConfigKind:  configKind,
		ServerCount: len(servers),
		Findings:    findings,
	}, nil
}

func analyzeFastDesktop(configPath string, content []byte) (Report, bool) {
	var root quickDesktopConfig
	if err := json.Unmarshal(content, &root); err != nil || root.MCPServers == nil {
		return Report{}, false
	}

	findings := make([]Finding, 0, len(root.MCPServers))
	for name, spec := range root.MCPServers {
		findings = append(findings, analyzeQuickServer(name, spec)...)
	}

	if len(findings) > 1 {
		sortFindings(findings)
	}

	return Report{
		ConfigPath:  configPath,
		ConfigKind:  KindDesktopConfig,
		ServerCount: len(root.MCPServers),
		Findings:    findings,
	}, true
}

func analyzeQuickServer(serverName string, spec quickServerSpec) []Finding {
	if spec.URL != "" {
		return validateRemoteServerQuick(serverName, spec)
	}

	if serverName == "" {
		serverName = defaultServerLabel
	}

	if spec.Command == "" {
		return []Finding{{
			Server:   serverName,
			Severity: SeverityError,
			Problem:  "missing command for local MCP server",
			Fix:      "Set the command field to an executable that can launch the server.",
		}}
	}

	if spec.Transport != "" && !strings.EqualFold(spec.Transport, "stdio") {
		return []Finding{{
			Server:   serverName,
			Severity: SeverityWarning,
			Problem:  fmt.Sprintf("local server uses non-stdio transport: %s", spec.Transport),
			Fix:      "Use stdio for local MCP servers or move the server behind a supported remote endpoint.",
		}}
	}

	return nil
}

func validateRemoteServerQuick(serverName string, spec quickServerSpec) []Finding {
	findings := make([]Finding, 0, 2)
	if serverName == "" {
		serverName = defaultServerLabel
	}

	parsedURL, err := url.Parse(spec.URL)
	if err != nil || parsedURL.Scheme == "" || parsedURL.Host == "" {
		return []Finding{{
			Server:   serverName,
			Severity: SeverityError,
			Problem:  fmt.Sprintf("invalid remote URL: %s", spec.URL),
			Fix:      "Set url to a full http or https endpoint.",
		}}
	}

	if strings.EqualFold(parsedURL.Scheme, "http") && !isLocalHost(parsedURL.Hostname()) {
		findings = append(findings, Finding{
			Server:   serverName,
			Severity: SeverityWarning,
			Problem:  fmt.Sprintf("remote endpoint uses plain HTTP: %s", spec.URL),
			Fix:      "Switch the endpoint to HTTPS unless it is local development.",
		})
	}

	if spec.Command != "" {
		findings = append(findings, Finding{
			Server:   serverName,
			Severity: SeverityWarning,
			Problem:  "remote server should not set a local command",
			Fix:      "Remove the command field from the remote server definition.",
		})
	}

	return findings
}

// FormatTextReport renders a human-readable report.
func FormatTextReport(report Report) string {
	var builder strings.Builder
	builder.Grow(64 + len(report.ConfigPath) + len(report.ConfigKind) + len(report.Findings)*96)
	builder.WriteString("mcp-smoke report\n")
	builder.WriteString("config: ")
	builder.WriteString(report.ConfigPath)
	builder.WriteString("\nformat: ")
	builder.WriteString(report.ConfigKind)
	builder.WriteString("\nservers: ")
	builder.WriteString(strconv.Itoa(report.ServerCount))
	builder.WriteString("\nfindings: ")
	builder.WriteString(strconv.Itoa(len(report.Findings)))
	builder.WriteString("\n\n")

	if len(report.Findings) == 0 {
		builder.WriteString("No blocking issues found.\n")
		return builder.String()
	}

	for index, finding := range report.Findings {
		builder.WriteString(strconv.Itoa(index + 1))
		builder.WriteString(". [")
		builder.WriteString(finding.Severity)
		builder.WriteString("] ")
		builder.WriteString(finding.Server)
		builder.WriteString("\n   problem: ")
		builder.WriteString(finding.Problem)
		builder.WriteString("\n   fix: ")
		builder.WriteString(finding.Fix)
		builder.WriteString("\n")
		if index < len(report.Findings)-1 {
			builder.WriteString("\n")
		}
	}

	return builder.String()
}

// FormatJSONReport renders the report as indented JSON.
func FormatJSONReport(report Report) (string, error) {
	data, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return "", fmt.Errorf("marshal report failed: %w", err)
	}
	return string(data), nil
}

func parseServers(content []byte) (string, []serverSpec, error) {
	if firstNonSpace(content) == '[' {
		return parseServerList(content)
	}

	var root configProbe
	if err := json.Unmarshal(content, &root); err != nil {
		return "", nil, fmt.Errorf("invalid JSON: %w", err)
	}

	if root.MCPServers != nil {
		return normalizeDesktopServers(root.MCPServers)
	}

	if root.Servers != nil {
		return normalizeServerList(root.Servers)
	}

	if len(root.Command) > 0 {
		return parseSingleServer(content)
	}

	if len(root.URL) > 0 {
		return parseSingleServer(content)
	}

	return "", nil, fmt.Errorf("unsupported config format: expected %q, %q, or a single server object", KindDesktopConfig, KindServerList)
}

func normalizeDesktopServers(servers map[string]serverSpec) (string, []serverSpec, error) {
	normalized := make([]serverSpec, 0, len(servers))
	for name, spec := range servers {
		spec.Name = name
		normalized = append(normalized, spec)
	}

	return KindDesktopConfig, normalized, nil
}

func parseServerList(raw json.RawMessage) (string, []serverSpec, error) {
	if firstNonSpace(raw) == '[' {
		var servers []serverSpec
		if err := json.Unmarshal(raw, &servers); err != nil {
			return "", nil, fmt.Errorf("decode %s failed: %w", KindServerList, err)
		}
		return normalizeServerList(servers)
	}

	var wrapped struct {
		Servers []serverSpec `json:"servers"`
	}
	if err := json.Unmarshal(raw, &wrapped); err != nil {
		return "", nil, fmt.Errorf("decode %s failed: %w", KindServerList, err)
	}

	return normalizeServerList(wrapped.Servers)
}

func parseSingleServer(raw []byte) (string, []serverSpec, error) {
	var spec serverSpec
	if err := json.Unmarshal(raw, &spec); err != nil {
		return "", nil, fmt.Errorf("decode %s failed: %w", KindSingleServer, err)
	}

	if spec.Name == "" {
		spec.Name = defaultServerLabel
	}

	return KindSingleServer, []serverSpec{spec}, nil
}

func normalizeServerList(servers []serverSpec) (string, []serverSpec, error) {
	normalized := make([]serverSpec, 0, len(servers))
	seen := make(map[string]struct{}, len(servers))
	for i := range servers {
		spec := servers[i]
		name := spec.Name
		if name == "" {
			name = defaultServerLabel
		}
		if _, ok := seen[name]; ok {
			return "", nil, fmt.Errorf("duplicate server name detected: %s", name)
		}
		seen[name] = struct{}{}
		spec.Name = name
		normalized = append(normalized, spec)
	}

	return KindServerList, normalized, nil
}

func analyzeServer(baseDir string, spec serverSpec, options Options) []Finding {
	if spec.URL != "" {
		return validateRemoteServer(spec)
	}

	findings := make([]Finding, 0, 4)
	serverName := spec.Name
	if serverName == "" {
		serverName = defaultServerLabel
	}

	if spec.Command == "" {
		findings = append(findings, Finding{
			Server:   serverName,
			Severity: SeverityError,
			Problem:  "missing command for local MCP server",
			Fix:      "Set the command field to an executable that can launch the server.",
		})
		return findings
	}

	if !options.SkipPath {
		if _, err := exec.LookPath(spec.Command); err != nil {
			findings = append(findings, Finding{
				Server:   serverName,
				Severity: SeverityError,
				Problem:  fmt.Sprintf("command not found: %s", spec.Command),
				Fix:      "Install the command or update the config to use a valid executable name.",
			})
		}
	}

	if !options.SkipCwd && spec.Cwd != "" {
		cwdPath := resolvePath(baseDir, spec.Cwd)
		info, err := os.Stat(cwdPath)
		if err != nil {
			findings = append(findings, Finding{
				Server:   serverName,
				Severity: SeverityError,
				Problem:  fmt.Sprintf("cwd does not exist: %s", cwdPath),
				Fix:      "Create the directory or point cwd at an existing workspace.",
			})
		} else if !info.IsDir() {
			findings = append(findings, Finding{
				Server:   serverName,
				Severity: SeverityError,
				Problem:  fmt.Sprintf("cwd is not a directory: %s", cwdPath),
				Fix:      "Change cwd to a directory path.",
			})
		}
	}

	if spec.Transport != "" && !strings.EqualFold(spec.Transport, "stdio") {
		findings = append(findings, Finding{
			Server:   serverName,
			Severity: SeverityWarning,
			Problem:  fmt.Sprintf("local server uses non-stdio transport: %s", spec.Transport),
			Fix:      "Use stdio for local MCP servers or move the server behind a supported remote endpoint.",
		})
	}

	if !options.SkipPath && shouldCheckScriptPath(spec.Command, spec.Args) && len(spec.Args) > 0 {
		scriptPath := resolveScriptPath(baseDir, spec.Cwd, spec.Args[0])
		if info, err := os.Stat(scriptPath); err != nil || info.IsDir() {
			findings = append(findings, Finding{
				Server:   serverName,
				Severity: SeverityError,
				Problem:  fmt.Sprintf("script file not found: %s", scriptPath),
				Fix:      "Create the script file or update the first argument to an existing path.",
			})
		}
	}

	return findings
}

func validateRemoteServer(spec serverSpec) []Finding {
	findings := make([]Finding, 0, 2)
	serverName := spec.Name
	if serverName == "" {
		serverName = defaultServerLabel
	}

	parsedURL, err := url.Parse(spec.URL)
	if err != nil || parsedURL.Scheme == "" || parsedURL.Host == "" {
		findings = append(findings, Finding{
			Server:   serverName,
			Severity: SeverityError,
			Problem:  fmt.Sprintf("invalid remote URL: %s", spec.URL),
			Fix:      "Set url to a full http or https endpoint.",
		})
		return findings
	}

	if strings.EqualFold(parsedURL.Scheme, "http") && !isLocalHost(parsedURL.Hostname()) {
		findings = append(findings, Finding{
			Server:   serverName,
			Severity: SeverityWarning,
			Problem:  fmt.Sprintf("remote endpoint uses plain HTTP: %s", spec.URL),
			Fix:      "Switch the endpoint to HTTPS unless it is local development.",
		})
	}

	if spec.Command != "" {
		findings = append(findings, Finding{
			Server:   serverName,
			Severity: SeverityWarning,
			Problem:  "remote server should not set a local command",
			Fix:      "Remove the command field from the remote server definition.",
		})
	}

	return findings
}

func resolvePath(baseDir, value string) string {
	if filepath.IsAbs(value) {
		return filepath.Clean(value)
	}
	return filepath.Clean(filepath.Join(baseDir, value))
}

func resolveScriptPath(baseDir, cwd, value string) string {
	if filepath.IsAbs(value) {
		return filepath.Clean(value)
	}
	if cwd != "" {
		return filepath.Clean(filepath.Join(resolvePath(baseDir, cwd), value))
	}
	return filepath.Clean(filepath.Join(baseDir, value))
}

func shouldCheckScriptPath(command string, args []string) bool {
	if len(args) == 0 {
		return false
	}

	switch strings.ToLower(filepath.Base(command)) {
	case "node", "npm", "npx", "pnpm", "bun", "python", "python3", "python3.11", "ruby", "deno":
		return true
	default:
		return strings.Contains(args[0], ".")
	}
}

func isLocalHost(host string) bool {
	switch {
	case host == "localhost":
		return true
	case strings.HasPrefix(host, "127."):
		return true
	case host == "::1":
		return true
	default:
		return false
	}
}

func sortFindings(findings []Finding) {
	if len(findings) < 2 {
		return
	}

	sort.Slice(findings, func(i, j int) bool {
		left := findings[i]
		right := findings[j]
		leftRank := severityRank(left.Severity)
		rightRank := severityRank(right.Severity)
		if leftRank != rightRank {
			return leftRank < rightRank
		}
		if left.Server != right.Server {
			return left.Server < right.Server
		}
		if left.Problem != right.Problem {
			return left.Problem < right.Problem
		}
		return left.Fix < right.Fix
	})
}

func severityRank(severity string) int {
	switch severity {
	case SeverityError:
		return 0
	case SeverityWarning:
		return 1
	default:
		return 2
	}
}

func firstNonSpace(content []byte) byte {
	for _, c := range content {
		switch c {
		case ' ', '\n', '\r', '\t':
			continue
		default:
			return c
		}
	}
	return 0
}
