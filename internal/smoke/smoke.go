package smoke

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
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
	Name      string
	Command   string
	Args      []string
	Cwd       string
	Env       map[string]string
	Transport string
	URL       string
}

type desktopConfig struct {
	MCPServers map[string]namedServerConfig `json:"mcpServers"`
}

type namedServerConfig struct {
	Name      string            `json:"name"`
	Command   string            `json:"command"`
	Args      []string          `json:"args"`
	Cwd       string            `json:"cwd"`
	Env       map[string]string `json:"env"`
	Transport string            `json:"transport"`
	URL       string            `json:"url"`
}

type serverListConfig struct {
	Servers []namedServerConfig `json:"servers"`
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

	sortFindings(findings)

	return Report{
		ConfigPath:  configPath,
		ConfigKind:  configKind,
		ServerCount: len(servers),
		Findings:    findings,
	}, nil
}

// FormatTextReport renders a human-readable report.
func FormatTextReport(report Report) string {
	var builder strings.Builder
	builder.WriteString("mcp-smoke report\n")
	builder.WriteString(fmt.Sprintf("config: %s\n", report.ConfigPath))
	builder.WriteString(fmt.Sprintf("format: %s\n", report.ConfigKind))
	builder.WriteString(fmt.Sprintf("servers: %d\n", report.ServerCount))
	builder.WriteString(fmt.Sprintf("findings: %d\n", len(report.Findings)))
	builder.WriteString("\n")

	if len(report.Findings) == 0 {
		builder.WriteString("No blocking issues found.\n")
		return builder.String()
	}

	for index, finding := range report.Findings {
		builder.WriteString(fmt.Sprintf("%d. [%s] %s\n", index+1, finding.Severity, finding.Server))
		builder.WriteString(fmt.Sprintf("   problem: %s\n", finding.Problem))
		builder.WriteString(fmt.Sprintf("   fix: %s\n", finding.Fix))
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
	var root map[string]json.RawMessage
	if err := json.Unmarshal(content, &root); err != nil {
		return "", nil, fmt.Errorf("invalid JSON: %w", err)
	}

	if raw, ok := root[KindDesktopConfig]; ok {
		return parseDesktopConfig(raw)
	}

	if raw, ok := root[KindServerList]; ok {
		return parseServerList(raw)
	}

	if _, ok := root["command"]; ok {
		return parseSingleServer(content)
	}

	if _, ok := root["url"]; ok {
		return parseSingleServer(content)
	}

	return "", nil, fmt.Errorf("unsupported config format: expected %q, %q, or a single server object", KindDesktopConfig, KindServerList)
}

func parseDesktopConfig(raw json.RawMessage) (string, []serverSpec, error) {
	var config desktopConfig
	if err := json.Unmarshal(raw, &config); err != nil {
		return "", nil, fmt.Errorf("decode %s failed: %w", KindDesktopConfig, err)
	}

	servers := make([]serverSpec, 0, len(config.MCPServers))
	for name, spec := range config.MCPServers {
		servers = append(servers, serverSpec{
			Name:      name,
			Command:   spec.Command,
			Args:      spec.Args,
			Cwd:       spec.Cwd,
			Env:       spec.Env,
			Transport: spec.Transport,
			URL:       spec.URL,
		})
	}

	return KindDesktopConfig, servers, nil
}

func parseServerList(raw json.RawMessage) (string, []serverSpec, error) {
	servers, err := decodeServerList(raw)
	if err != nil {
		return "", nil, fmt.Errorf("decode %s failed: %w", KindServerList, err)
	}

	normalized := make([]serverSpec, 0, len(servers))
	seen := make(map[string]struct{}, len(servers))
	for _, spec := range servers {
		name := spec.Name
		if name == "" {
			name = defaultServerLabel
		}
		if _, ok := seen[name]; ok {
			return "", nil, fmt.Errorf("duplicate server name detected: %s", name)
		}
		seen[name] = struct{}{}
		normalized = append(normalized, serverSpec{
			Name:      name,
			Command:   spec.Command,
			Args:      spec.Args,
			Cwd:       spec.Cwd,
			Env:       spec.Env,
			Transport: spec.Transport,
			URL:       spec.URL,
		})
	}

	return KindServerList, normalized, nil
}

func parseSingleServer(raw []byte) (string, []serverSpec, error) {
	var spec namedServerConfig
	if err := json.Unmarshal(raw, &spec); err != nil {
		return "", nil, fmt.Errorf("decode %s failed: %w", KindSingleServer, err)
	}

	name := spec.Name
	if name == "" {
		name = defaultServerLabel
	}

	return KindSingleServer, []serverSpec{{
		Name:      name,
		Command:   spec.Command,
		Args:      spec.Args,
		Cwd:       spec.Cwd,
		Env:       spec.Env,
		Transport: spec.Transport,
		URL:       spec.URL,
	}}, nil
}

func decodeServerList(raw json.RawMessage) ([]namedServerConfig, error) {
	var direct []namedServerConfig
	if err := json.Unmarshal(raw, &direct); err == nil {
		return direct, nil
	}

	var wrapped serverListConfig
	if err := json.Unmarshal(raw, &wrapped); err != nil {
		return nil, err
	}

	return wrapped.Servers, nil
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

	if !options.SkipEnv {
		for key, value := range spec.Env {
			if strings.TrimSpace(value) == "" {
				findings = append(findings, Finding{
					Server:   serverName,
					Severity: SeverityWarning,
					Problem:  fmt.Sprintf("env %s is empty", key),
					Fix:      "Set the environment variable before running the server.",
				})
			}
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

	if shouldCheckScriptPath(spec.Command, spec.Args) && len(spec.Args) > 0 && !options.SkipPath {
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
	sort.SliceStable(findings, func(i, j int) bool {
		left := findings[i]
		right := findings[j]
		if severityRank(left.Severity) != severityRank(right.Severity) {
			return severityRank(left.Severity) < severityRank(right.Severity)
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
	switch strings.ToLower(severity) {
	case SeverityError:
		return 0
	case SeverityWarning:
		return 1
	default:
		return 2
	}
}
