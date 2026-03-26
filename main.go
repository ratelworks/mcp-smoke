package main

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/ratelworks/mcp-smoke/internal/smoke"
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

	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			if i+1 < len(args) {
				if _, err := os.Stderr.WriteString("unexpected argument: " + args[i+1] + "\n"); err != nil {
					return smoke.ExitCodeSystemError
				}
				return smoke.ExitCodeUserError
			}
			break
		}

		if !strings.HasPrefix(arg, "-") || arg == "-" {
			if _, err := os.Stderr.WriteString("unexpected argument: " + arg + "\n"); err != nil {
				return smoke.ExitCodeSystemError
			}
			return smoke.ExitCodeUserError
		}

		key, value, hasValue := strings.Cut(arg, "=")
		switch key {
		case "-config", "--config":
			if hasValue {
				configPath = value
				continue
			}
			if i+1 >= len(args) {
				if _, err := os.Stderr.WriteString("flag needs an argument: -config\n"); err != nil {
					return smoke.ExitCodeSystemError
				}
				return smoke.ExitCodeUserError
			}
			i++
			configPath = args[i]
		case "-json", "--json":
			valueBool, err := parseBoolFlag(hasValue, value)
			if err != nil {
				if _, writeErr := os.Stderr.WriteString(err.Error() + "\n"); writeErr != nil {
					return smoke.ExitCodeSystemError
				}
				return smoke.ExitCodeUserError
			}
			jsonOutput = valueBool
		case "-skip-cwd", "--skip-cwd":
			valueBool, err := parseBoolFlag(hasValue, value)
			if err != nil {
				if _, writeErr := os.Stderr.WriteString(err.Error() + "\n"); writeErr != nil {
					return smoke.ExitCodeSystemError
				}
				return smoke.ExitCodeUserError
			}
			skipCwd = valueBool
		case "-skip-env", "--skip-env":
			valueBool, err := parseBoolFlag(hasValue, value)
			if err != nil {
				if _, writeErr := os.Stderr.WriteString(err.Error() + "\n"); writeErr != nil {
					return smoke.ExitCodeSystemError
				}
				return smoke.ExitCodeUserError
			}
			skipEnv = valueBool
		case "-skip-path", "--skip-path":
			valueBool, err := parseBoolFlag(hasValue, value)
			if err != nil {
				if _, writeErr := os.Stderr.WriteString(err.Error() + "\n"); writeErr != nil {
					return smoke.ExitCodeSystemError
				}
				return smoke.ExitCodeUserError
			}
			skipPath = valueBool
		default:
			if _, err := os.Stderr.WriteString("flag provided but not defined: " + arg + "\n"); err != nil {
				return smoke.ExitCodeSystemError
			}
			return smoke.ExitCodeUserError
		}
	}

	if configPath == "" {
		if _, err := os.Stderr.WriteString("config path is required\n"); err != nil {
			return smoke.ExitCodeSystemError
		}
		return smoke.ExitCodeUserError
	}

	report, err := smoke.AnalyzeFile(configPath, smoke.Options{
		SkipCwd:  skipCwd,
		SkipEnv:  skipEnv,
		SkipPath: skipPath,
	})
	if err != nil {
		if _, writeErr := os.Stderr.WriteString(err.Error() + "\n"); writeErr != nil {
			return smoke.ExitCodeSystemError
		}

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
		if _, writeErr := os.Stderr.WriteString(err.Error() + "\n"); writeErr != nil {
			return smoke.ExitCodeSystemError
		}
		return smoke.ExitCodeSystemError
	}

	if _, err := os.Stdout.WriteString(output); err != nil {
		if _, writeErr := os.Stderr.WriteString("write report failed: " + err.Error() + "\n"); writeErr != nil {
			return smoke.ExitCodeSystemError
		}
		return smoke.ExitCodeSystemError
	}

	if len(report.Findings) > 0 {
		fmt.Fprintf(os.Stderr, "smoke check failed with %d finding(s)\n", len(report.Findings))
		return smoke.ExitCodeUserError
	}

	return smoke.ExitCodeSuccess
}

func parseBoolFlag(hasValue bool, value string) (bool, error) {
	if !hasValue {
		return true, nil
	}

	parsed, err := strconv.ParseBool(value)
	if err != nil {
		return false, fmt.Errorf("invalid boolean value %q", value)
	}
	return parsed, nil
}
