package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/ratelworks/mcp-smoke/internal/smoke"
)

const (
	commandName = "mcp-smoke"
)

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	var configPath string
	var jsonOutput bool
	var skipCwd bool
	var skipEnv bool
	var skipPath bool

	flagSet := flag.NewFlagSet(commandName, flag.ContinueOnError)
	flagSet.SetOutput(io.Discard)
	flagSet.StringVar(&configPath, "config", "", "Path to an MCP client config file.")
	flagSet.BoolVar(&jsonOutput, "json", false, "Render the report as JSON.")
	flagSet.BoolVar(&skipCwd, "skip-cwd", false, "Skip checks for cwd entries.")
	flagSet.BoolVar(&skipEnv, "skip-env", false, "Skip checks for env entries.")
	flagSet.BoolVar(&skipPath, "skip-path", false, "Skip command and script path checks.")

	if err := flagSet.Parse(args); err != nil {
		fmt.Fprintln(os.Stderr, err.Error())
		return smoke.ExitCodeUserError
	}

	if tail := flagSet.Args(); len(tail) > 0 {
		fmt.Fprintf(os.Stderr, "unexpected argument: %s\n", tail[0])
		return smoke.ExitCodeUserError
	}

	if configPath == "" {
		fmt.Fprintln(os.Stderr, "config path is required")
		return smoke.ExitCodeUserError
	}

	report, err := smoke.AnalyzeFile(configPath, smoke.Options{
		SkipCwd:  skipCwd,
		SkipEnv:  skipEnv,
		SkipPath: skipPath,
	})
	if err != nil {
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

	var output string
	if jsonOutput {
		output, err = smoke.FormatJSONReport(report)
	} else {
		output = smoke.FormatTextReport(report)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err.Error())
		return smoke.ExitCodeSystemError
	}

	if _, err := io.WriteString(os.Stdout, output); err != nil {
		fmt.Fprintf(os.Stderr, "write report failed: %v\n", err)
		return smoke.ExitCodeSystemError
	}

	if len(report.Findings) > 0 {
		fmt.Fprintf(os.Stderr, "smoke check failed with %d finding(s)\n", len(report.Findings))
		return smoke.ExitCodeUserError
	}

	return smoke.ExitCodeSuccess
}
