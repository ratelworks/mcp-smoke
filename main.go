package main

import (
	"errors"
	"fmt"
	"os"

	"github.com/ratelworks/mcp-smoke/internal/smoke"
	"github.com/spf13/cobra"
)

func main() {
	if err := newRootCmd().Execute(); err != nil {
		os.Exit(exitCode(err))
	}
}

func newRootCmd() *cobra.Command {
	var configPath string
	var jsonOutput bool
	var skipCwd bool
	var skipEnv bool
	var skipPath bool
	var liveMode bool

	cmd := &cobra.Command{
		Use:     "mcp-smoke",
		Short:   "Smoke test MCP client configs for common failures.",
		Long:    "mcp-smoke reads a client config from disk, checks each MCP server definition, and reports concrete fixes for missing files, missing commands, and unsafe remote endpoints.",
		Version: "0.2.0",
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			if configPath == "" {
				return fmt.Errorf("--config is required")
			}

			report, err := smoke.AnalyzeFile(configPath, smoke.Options{
				SkipCwd:  skipCwd,
				SkipEnv:  skipEnv,
				SkipPath: skipPath,
				Live:     liveMode,
			})
			if err != nil {
				return err
			}

			var output string
			if jsonOutput {
				output, err = smoke.FormatJSONReport(report)
				if err != nil {
					return fmt.Errorf("render report failed: %w", err)
				}
			} else {
				output = smoke.FormatTextReport(report)
			}

			fmt.Print(output)

			if len(report.Findings) > 0 {
				return fmt.Errorf("smoke check failed with %d finding(s)", len(report.Findings))
			}
			return nil
		},
	}

	cmd.Flags().StringVar(&configPath, "config", "", "Path to an MCP client config file (required)")
	cmd.Flags().BoolVar(&jsonOutput, "json", false, "Print the report as JSON")
	cmd.Flags().BoolVar(&skipCwd, "skip-cwd", false, "Skip working directory checks")
	cmd.Flags().BoolVar(&skipEnv, "skip-env", false, "Skip environment variable checks")
	cmd.Flags().BoolVar(&skipPath, "skip-path", false, "Skip command and script path checks")
	cmd.Flags().BoolVar(&liveMode, "live", false, "Start each stdio server and verify MCP handshake")

	return cmd
}

func exitCode(err error) int {
	var appErr *smoke.AppError
	if errors.As(err, &appErr) {
		if appErr.Kind == smoke.ErrorKindUser {
			return smoke.ExitCodeUserError
		}
		return smoke.ExitCodeSystemError
	}
	return smoke.ExitCodeUserError
}
