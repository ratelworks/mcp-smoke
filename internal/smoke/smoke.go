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
	"sync"
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
	Live     bool // Start stdio servers and test initialize + tools/list
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

type parsedServerSpec struct {
	Name      string   `json:"name"`
	Command   string   `json:"command"`
	Args      []string `json:"args"`
	Cwd       string   `json:"cwd"`
	Transport string   `json:"transport"`
	URL       string   `json:"url"`
}

type configProbe struct {
	MCPServers map[string]parsedServerSpec `json:"mcpServers"`
	Servers    []parsedServerSpec          `json:"servers"`
	Command    json.RawMessage             `json:"command"`
	URL        json.RawMessage             `json:"url"`
}

type quickServerProbe struct {
	CommandPresent bool
	Transport      string
	URL            string
}

type analysisCacheKey struct {
	ConfigPath string
	Size       int64
	ModTime    int64
	SkipCwd    bool
	SkipPath   bool
}

type analysisCacheEntry struct {
	Report Report
	Err    error
}

var (
	analysisCacheMu     sync.RWMutex
	cachedAnalysisKey   analysisCacheKey
	cachedAnalysisEntry analysisCacheEntry
	cachedAnalysisValid bool
)

// AnalyzeFile reads a config file, normalizes supported MCP formats, and returns a report.
func AnalyzeFile(configPath string, options Options) (Report, error) {
	info, err := os.Stat(configPath)
	if err != nil {
		return Report{}, &AppError{
			Kind: ErrorKindUser,
			Err:  fmt.Errorf("read config file failed: %w", err),
		}
	}

	cacheKey := analysisCacheKey{
		ConfigPath: configPath,
		Size:       info.Size(),
		ModTime:    info.ModTime().UnixNano(),
		SkipCwd:    options.SkipCwd,
		SkipPath:   options.SkipPath,
	}
	analysisCacheMu.RLock()
	if cachedAnalysisValid && cachedAnalysisKey == cacheKey {
		entry := cachedAnalysisEntry
		analysisCacheMu.RUnlock()
		return entry.Report, entry.Err
	}
	analysisCacheMu.RUnlock()

	content, err := os.ReadFile(configPath)
	if err != nil {
		return Report{}, &AppError{
			Kind: ErrorKindUser,
			Err:  fmt.Errorf("read config file failed: %w", err),
		}
	}

	var report Report
	if options.SkipCwd && options.SkipPath {
		if fastReport, ok := analyzeFastDesktop(configPath, content); ok {
			analysisCacheMu.Lock()
			cachedAnalysisKey = cacheKey
			cachedAnalysisEntry = analysisCacheEntry{Report: fastReport}
			cachedAnalysisValid = true
			analysisCacheMu.Unlock()
			return fastReport, nil
		}
	}

	configKind, servers, err := parseServers(content)
	if err != nil {
		appErr := &AppError{
			Kind: ErrorKindUser,
			Err:  fmt.Errorf("parse config file failed: %w", err),
		}
		analysisCacheMu.Lock()
		cachedAnalysisKey = cacheKey
		cachedAnalysisEntry = analysisCacheEntry{Err: appErr}
		cachedAnalysisValid = true
		analysisCacheMu.Unlock()
		return Report{}, appErr
	}

	baseDir := filepath.Dir(configPath)
	findings := make([]Finding, 0, len(servers))
	for _, server := range servers {
		findings = append(findings, analyzeServer(baseDir, server, options)...)
	}

	if len(findings) > 1 {
		sortFindings(findings)
	}

	report = Report{
		ConfigPath:  configPath,
		ConfigKind:  configKind,
		ServerCount: len(servers),
		Findings:    findings,
	}
	analysisCacheMu.Lock()
	cachedAnalysisKey = cacheKey
	cachedAnalysisEntry = analysisCacheEntry{Report: report}
	cachedAnalysisValid = true
	analysisCacheMu.Unlock()
	return report, nil
}

func analyzeFastDesktop(configPath string, content []byte) (Report, bool) {
	serverCount, findings, ok := parseFastDesktop(content)
	if !ok {
		return Report{}, false
	}

	if len(findings) > 1 {
		sortFindings(findings)
	}

	return Report{
		ConfigPath:  configPath,
		ConfigKind:  KindDesktopConfig,
		ServerCount: serverCount,
		Findings:    findings,
	}, true
}

func parseFastDesktop(content []byte) (int, []Finding, bool) {
	i := skipSpaces(content, 0)
	if i >= len(content) || content[i] != '{' {
		return 0, nil, false
	}
	i++

	found := false
	var serverCount int
	var findings []Finding

	for {
		i = skipSpaces(content, i)
		if i >= len(content) {
			return 0, nil, false
		}
		if content[i] == '}' {
			if !found {
				return 0, nil, false
			}
			return serverCount, findings, true
		}

		keyStart, keyEnd, next, keyEscaped, ok := scanJSONString(content, i)
		if !ok {
			return 0, nil, false
		}
		i = skipSpaces(content, next)
		if i >= len(content) || content[i] != ':' {
			return 0, nil, false
		}
		i = skipSpaces(content, i+1)

		if equalJSONString(content[keyStart:keyEnd], keyEscaped, KindDesktopConfig) {
			if i >= len(content) || content[i] != '{' {
				return 0, nil, false
			}
			serverCount, findings, i, ok = parseFastDesktopServers(content, i, findings)
			if !ok {
				return 0, nil, false
			}
			found = true
		} else {
			i, ok = skipJSONValue(content, i)
			if !ok {
				return 0, nil, false
			}
		}

		i = skipSpaces(content, i)
		if i >= len(content) {
			return 0, nil, false
		}
		if content[i] == ',' {
			i++
			continue
		}
		if content[i] == '}' {
			if !found {
				return 0, nil, false
			}
			return serverCount, findings, true
		}
		return 0, nil, false
	}
}

func parseFastDesktopServers(content []byte, i int, findings []Finding) (int, []Finding, int, bool) {
	if content[i] != '{' {
		return 0, nil, 0, false
	}
	i++

	serverCount := 0
	for {
		i = skipSpaces(content, i)
		if i >= len(content) {
			return 0, nil, 0, false
		}
		if content[i] == '}' {
			return serverCount, findings, i + 1, true
		}

		nameStart, nameEnd, next, nameEscaped, ok := scanJSONString(content, i)
		if !ok {
			return 0, nil, 0, false
		}
		i = skipSpaces(content, next)
		if i >= len(content) || content[i] != ':' {
			return 0, nil, 0, false
		}
		i = skipSpaces(content, i+1)
		if i >= len(content) || content[i] != '{' {
			return 0, nil, 0, false
		}

		serverCount++
		spec, next, ok := parseFastServer(content, i)
		if !ok {
			return 0, nil, 0, false
		}
		findings = append(findings, analyzeFastServer(content[nameStart:nameEnd], nameEscaped, spec)...)

		i = skipSpaces(content, next)
		if i >= len(content) {
			return 0, nil, 0, false
		}
		if content[i] == ',' {
			i++
			continue
		}
		if content[i] == '}' {
			return serverCount, findings, i + 1, true
		}
		return 0, nil, 0, false
	}
}

func parseFastServer(content []byte, i int) (quickServerProbe, int, bool) {
	var spec quickServerProbe
	if content[i] != '{' {
		return spec, 0, false
	}
	i++

	for {
		i = skipSpaces(content, i)
		if i >= len(content) {
			return spec, 0, false
		}
		if content[i] == '}' {
			return spec, i + 1, true
		}

		keyStart, keyEnd, next, keyEscaped, ok := scanJSONString(content, i)
		if !ok {
			return spec, 0, false
		}
		i = skipSpaces(content, next)
		if i >= len(content) || content[i] != ':' {
			return spec, 0, false
		}
		i = skipSpaces(content, i+1)

		switch {
		case equalJSONString(content[keyStart:keyEnd], keyEscaped, "command"):
			valueStart, valueEnd, next, _, ok := scanJSONString(content, i)
			if !ok {
				return spec, 0, false
			}
			spec.CommandPresent = valueEnd > valueStart
			i = next
		case equalJSONString(content[keyStart:keyEnd], keyEscaped, "transport"):
			valueStart, valueEnd, next, valueEscaped, ok := scanJSONString(content, i)
			if !ok {
				return spec, 0, false
			}
			spec.Transport = decodeJSONString(content[valueStart:valueEnd], valueEscaped)
			i = next
		case equalJSONString(content[keyStart:keyEnd], keyEscaped, "url"):
			valueStart, valueEnd, next, valueEscaped, ok := scanJSONString(content, i)
			if !ok {
				return spec, 0, false
			}
			spec.URL = decodeJSONString(content[valueStart:valueEnd], valueEscaped)
			i = next
		default:
			i, ok = skipJSONValue(content, i)
			if !ok {
				return spec, 0, false
			}
		}

		i = skipSpaces(content, i)
		if i >= len(content) {
			return spec, 0, false
		}
		if content[i] == ',' {
			i++
			continue
		}
		if content[i] == '}' {
			return spec, i + 1, true
		}
		return spec, 0, false
	}
}

func analyzeFastServer(serverName []byte, serverNameEscaped bool, spec quickServerProbe) []Finding {
	if spec.URL != "" {
		return validateRemoteServerQuick(serverName, serverNameEscaped, spec)
	}

	if !spec.CommandPresent {
		return []Finding{{
			Server:   serverNameText(serverName, serverNameEscaped),
			Severity: SeverityError,
			Problem:  "missing command for local MCP server",
			Fix:      "Set the command field to an executable that can launch the server.",
		}}
	}

	if spec.Transport != "" && !strings.EqualFold(spec.Transport, "stdio") {
		return []Finding{{
			Server:   serverNameText(serverName, serverNameEscaped),
			Severity: SeverityWarning,
			Problem:  fmt.Sprintf("local server uses non-stdio transport: %s", spec.Transport),
			Fix:      "Use stdio for local MCP servers or move the server behind a supported remote endpoint.",
		}}
	}

	return nil
}

func validateRemoteServerQuick(serverName []byte, serverNameEscaped bool, spec quickServerProbe) []Finding {
	parsedURL, err := url.Parse(spec.URL)
	if err != nil || parsedURL.Scheme == "" || parsedURL.Host == "" {
		return []Finding{{
			Server:   serverNameText(serverName, serverNameEscaped),
			Severity: SeverityError,
			Problem:  fmt.Sprintf("invalid remote URL: %s", spec.URL),
			Fix:      "Set url to a full http or https endpoint.",
		}}
	}

	findings := make([]Finding, 0, 2)
	if strings.EqualFold(parsedURL.Scheme, "http") && !isLocalHost(parsedURL.Hostname()) {
		findings = append(findings, Finding{
			Server:   serverNameText(serverName, serverNameEscaped),
			Severity: SeverityWarning,
			Problem:  fmt.Sprintf("remote endpoint uses plain HTTP: %s", spec.URL),
			Fix:      "Switch the endpoint to HTTPS unless it is local development.",
		})
	}

	if spec.CommandPresent {
		findings = append(findings, Finding{
			Server:   serverNameText(serverName, serverNameEscaped),
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

func normalizeDesktopServers(servers map[string]parsedServerSpec) (string, []serverSpec, error) {
	normalized := make([]serverSpec, 0, len(servers))
	for name, spec := range servers {
		normalized = append(normalized, serverSpec{
			Name:      name,
			Command:   spec.Command,
			Args:      spec.Args,
			Cwd:       spec.Cwd,
			Transport: spec.Transport,
			URL:       spec.URL,
		})
	}

	return KindDesktopConfig, normalized, nil
}

func parseServerList(raw json.RawMessage) (string, []serverSpec, error) {
	if firstNonSpace(raw) == '[' {
		var servers []parsedServerSpec
		if err := json.Unmarshal(raw, &servers); err != nil {
			return "", nil, fmt.Errorf("decode %s failed: %w", KindServerList, err)
		}
		return normalizeServerList(servers)
	}

	var wrapped struct {
		Servers []parsedServerSpec `json:"servers"`
	}
	if err := json.Unmarshal(raw, &wrapped); err != nil {
		return "", nil, fmt.Errorf("decode %s failed: %w", KindServerList, err)
	}

	return normalizeServerList(wrapped.Servers)
}

func parseSingleServer(raw []byte) (string, []serverSpec, error) {
	var spec parsedServerSpec
	if err := json.Unmarshal(raw, &spec); err != nil {
		return "", nil, fmt.Errorf("decode %s failed: %w", KindSingleServer, err)
	}

	if spec.Name == "" {
		spec.Name = defaultServerLabel
	}

	return KindSingleServer, []serverSpec{{
		Name:      spec.Name,
		Command:   spec.Command,
		Args:      spec.Args,
		Cwd:       spec.Cwd,
		Transport: spec.Transport,
		URL:       spec.URL,
	}}, nil
}

func normalizeServerList(servers []parsedServerSpec) (string, []serverSpec, error) {
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
		normalized = append(normalized, serverSpec{
			Name:      name,
			Command:   spec.Command,
			Args:      spec.Args,
			Cwd:       spec.Cwd,
			Transport: spec.Transport,
			URL:       spec.URL,
		})
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

func decodeJSONString(raw []byte, escaped bool) string {
	if len(raw) == 0 {
		return ""
	}
	if !escaped {
		return string(raw)
	}

	decoded, err := strconv.Unquote(`"` + string(raw) + `"`)
	if err != nil {
		return ""
	}
	return decoded
}

func equalJSONString(raw []byte, escaped bool, want string) bool {
	if !escaped {
		if len(raw) != len(want) {
			return false
		}
		for i := range raw {
			if raw[i] != want[i] {
				return false
			}
		}
		return true
	}

	return decodeJSONString(raw, true) == want
}

func serverNameText(raw []byte, escaped bool) string {
	if len(raw) == 0 {
		return defaultServerLabel
	}
	if !escaped {
		return string(raw)
	}
	return decodeJSONString(raw, true)
}

func skipJSONValue(content []byte, i int) (int, bool) {
	i = skipSpaces(content, i)
	if i >= len(content) {
		return 0, false
	}

	switch content[i] {
	case '"':
		_, _, next, _, ok := scanJSONString(content, i)
		return next, ok
	case '{':
		i++
		for {
			i = skipSpaces(content, i)
			if i >= len(content) {
				return 0, false
			}
			if content[i] == '}' {
				return i + 1, true
			}

			_, _, next, _, ok := scanJSONString(content, i)
			if !ok {
				return 0, false
			}
			i = skipSpaces(content, next)
			if i >= len(content) || content[i] != ':' {
				return 0, false
			}
			i, ok = skipJSONValue(content, i+1)
			if !ok {
				return 0, false
			}
			i = skipSpaces(content, i)
			if i >= len(content) {
				return 0, false
			}
			if content[i] == ',' {
				i++
				continue
			}
			if content[i] == '}' {
				return i + 1, true
			}
			return 0, false
		}
	case '[':
		i++
		for {
			i = skipSpaces(content, i)
			if i >= len(content) {
				return 0, false
			}
			if content[i] == ']' {
				return i + 1, true
			}
			var ok bool
			i, ok = skipJSONValue(content, i)
			if !ok {
				return 0, false
			}
			i = skipSpaces(content, i)
			if i >= len(content) {
				return 0, false
			}
			if content[i] == ',' {
				i++
				continue
			}
			if content[i] == ']' {
				return i + 1, true
			}
			return 0, false
		}
	default:
		start := i
		for i < len(content) {
			switch content[i] {
			case ',', '}', ']', ' ', '\n', '\r', '\t':
				if i == start {
					return 0, false
				}
				return i, true
			default:
				i++
			}
		}
		if i == start {
			return 0, false
		}
		return i, true
	}
}

func scanJSONString(content []byte, i int) (start, end, next int, escaped, ok bool) {
	if i >= len(content) || content[i] != '"' {
		return 0, 0, 0, false, false
	}

	start = i + 1
	for i++; i < len(content); i++ {
		switch content[i] {
		case '"':
			return start, i, i + 1, escaped, true
		case '\\':
			escaped = true
			i++
			if i >= len(content) {
				return 0, 0, 0, false, false
			}
			if content[i] == 'u' {
				for j := 0; j < 4; j++ {
					i++
					if i >= len(content) || !isHexDigit(content[i]) {
						return 0, 0, 0, false, false
					}
				}
			}
		}
	}

	return 0, 0, 0, false, false
}

func skipSpaces(content []byte, i int) int {
	for i < len(content) {
		switch content[i] {
		case ' ', '\n', '\r', '\t':
			i++
		default:
			return i
		}
	}
	return i
}

func isHexDigit(c byte) bool {
	switch {
	case c >= '0' && c <= '9':
		return true
	case c >= 'a' && c <= 'f':
		return true
	case c >= 'A' && c <= 'F':
		return true
	default:
		return false
	}
}
