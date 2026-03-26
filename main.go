package main

import (
	"errors"
	"fmt"
	"os"

	"github.com/ratelworks/mcp-smoke/internal/smoke"
	"github.com/spf13/cobra"
)

const (
	commandName = "mcp-smoke"
)

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	rootCmd := newRootCommand()
	rootCmd.SetArgs(args)

	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err.Error())

		var appErr *smoke.AppError
		if errors.As(err, &appErr) {
			if appErr.Kind == smoke.ErrorKindUser {
				return smoke.ExitCodeUserError
			}
			return smoke.ExitCodeSystemError
		}

		return smoke.ExitCodeUserError
	}

	return smoke.ExitCodeSuccess
}

func newRootCommand() *cobra.Command {
	var configPath string
	var jsonOutput bool
	var skipCwd bool
	var skipEnv bool
	var skipPath bool

	cmd := &cobra.Command{
		Use:           commandName,
		Short:         "Smoke test MCP client configs for common failures.",
		Long:          "mcp-smoke reads a client config from disk, checks each MCP server definition, and reports concrete fixes for missing files, missing commands, and unsafe remote endpoints.",
		SilenceErrors: true,
		SilenceUsage:  true,
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) > 0 {
				return &smoke.AppError{
					Kind: smoke.ErrorKindUser,
					Err:  fmt.Errorf("unexpected argument: %s", args[0]),
				}
			}

			if configPath == "" {
				return &smoke.AppError{
					Kind: smoke.ErrorKindUser,
					Err:  fmt.Errorf("config path is required"),
				}
			}

			report, err := smoke.AnalyzeFile(configPath, smoke.Options{
				SkipCwd:  skipCwd,
				SkipEnv:  skipEnv,
				SkipPath: skipPath,
			})
			if err != nil {
				return err
			}

			var output string
			if jsonOutput {
				output, err = smoke.FormatJSONReport(report)
			} else {
				output = smoke.FormatTextReport(report)
			}
			if err != nil {
				return &smoke.AppError{
					Kind: smoke.ErrorKindSystem,
					Err:  fmt.Errorf("render report failed: %w", err),
				}
			}

			if _, err := fmt.Fprint(cmd.OutOrStdout(), output); err != nil {
				return &smoke.AppError{
					Kind: smoke.ErrorKindSystem,
					Err:  fmt.Errorf("write report failed: %w", err),
				}
			}

			if len(report.Findings) > 0 {
				return &smoke.AppError{
					Kind: smoke.ErrorKindUser,
					Err:  fmt.Errorf("smoke check failed with %d finding(s)", len(report.Findings)),
				}
			}

			return nil
		},
	}

	cmd.Flags().StringVar(&configPath, "config", "", "Path to an MCP client config file.")
	cmd.Flags().BoolVar(&jsonOutput, "json", false, "Render the report as JSON.")
	cmd.Flags().BoolVar(&skipCwd, "skip-cwd", false, "Skip checks for cwd entries.")
	cmd.Flags().BoolVar(&skipEnv, "skip-env", false, "Skip checks for env entries.")
	cmd.Flags().BoolVar(&skipPath, "skip-path", false, "Skip command and script path checks.")
	if err := cmd.MarkFlagRequired("config"); err != nil {
		panic(err)
	}

	return cmd
}
