package smoke

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAnalyzeFile(t *testing.T) {
	binaryPath, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable failed: %v", err)
	}

	tests := []struct {
		name         string
		config       string
		wantKind     string
		wantFindings int
		wantErr      bool
		wantErrPart  string
	}{
		{
			name: "desktop config with multiple issues",
			config: `{
  "mcpServers": {
    "filesystem": {
      "command": "definitely-missing-mcp-server",
      "args": ["server.js"],
      "cwd": "./missing-workspace",
      "env": {
        "API_KEY": "",
        "REGION": "us-central1"
      }
    },
    "preview": {
      "url": "http://example.com/mcp"
    }
  }
}`,
			wantKind:     KindDesktopConfig,
			wantFindings: 4,
		},
		{
			name: "single server config without findings",
			config: `{
  "name": "clean-server",
  "command": "` + binaryPath + `",
  "cwd": ".",
  "env": {
    "REGION": "us-central1"
  }
}`,
			wantKind:     KindSingleServer,
			wantFindings: 0,
		},
		{
			name: "server list with insecure remote endpoint",
			config: `{
  "servers": [
    {
      "name": "preview",
      "url": "http://example.com/mcp"
    }
  ]
}`,
			wantKind:     KindServerList,
			wantFindings: 1,
		},
		{
			name:        "invalid json",
			config:      `{"mcpServers":`,
			wantErr:     true,
			wantErrPart: "parse config file failed",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			configPath := filepath.Join(dir, "config.json")
			if err := os.WriteFile(configPath, []byte(tt.config), 0o600); err != nil {
				t.Fatalf("write config failed: %v", err)
			}

			report, err := AnalyzeFile(configPath, Options{})
			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected error")
				}
				if !strings.Contains(err.Error(), tt.wantErrPart) {
					t.Fatalf("expected error to contain %q, got %q", tt.wantErrPart, err.Error())
				}
				return
			}

			if err != nil {
				t.Fatalf("AnalyzeFile failed: %v", err)
			}
			if report.ConfigKind != tt.wantKind {
				t.Fatalf("expected config kind %q, got %q", tt.wantKind, report.ConfigKind)
			}
			if len(report.Findings) != tt.wantFindings {
				t.Fatalf("expected %d findings, got %d", tt.wantFindings, len(report.Findings))
			}
		})
	}
}
