<h1 align="center">mcp-smoke</h1>

MCP configs drift quickly and fail in different ways on every machine.
mcp-smoke reads your saved client config from disk, checks each server, and shows concrete fixes before you waste time on startup errors.

[![CI](https://github.com/ratelworks/mcp-smoke/actions/workflows/ci.yml/badge.svg)](https://github.com/ratelworks/mcp-smoke/actions/workflows/ci.yml)
[![Latest Release](https://img.shields.io/github/v/release/ratelworks/mcp-smoke)](https://github.com/ratelworks/mcp-smoke/releases)
[![License](https://img.shields.io/github/license/ratelworks/mcp-smoke)](LICENSE)
[![Go Reference](https://pkg.go.dev/badge/github.com/ratelworks/mcp-smoke.svg)](https://pkg.go.dev/github.com/ratelworks/mcp-smoke)

## The Problem
On Monday a local MCP (Model Context Protocol) config points at a working server command and a valid workspace.
On Wednesday someone renames the script, moves the workspace, or clears an environment variable.
On Friday the agent starts up, the server never comes online, and the failure is buried behind a generic startup message.
mcp-smoke replays the config from disk, checks the obvious breakpoints, and turns that silence into a short list of fixes.
This is the same problem that `docker compose config` solved for container stacks.

## What It Does
Run one command against a config file and get specific findings back:

```bash
$ mcp-smoke --config examples/mcp.example.json
mcp-smoke report
config: examples/mcp.example.json
format: mcpServers
servers: 2
findings: 4

1. [error] filesystem
   problem: command not found: definitely-missing-mcp-server
   fix: Install the command or update the config to use a valid executable name.

2. [error] filesystem
   problem: cwd does not exist: examples/missing-workspace
   fix: Create the directory or point cwd at an existing workspace.

3. [error] filesystem
   problem: script file not found: examples/server.js
   fix: Create the script file or update the first argument to an existing path.

4. [warning] preview
   problem: remote endpoint uses plain HTTP: http://example.com/mcp
   fix: Switch the endpoint to HTTPS unless it is local development.
```

## Getting Started

### Step 1: Install
```bash
go install github.com/ratelworks/mcp-smoke@latest
```

### Step 2: Create config
Use any supported MCP config shape. The example below uses the `mcpServers` format.

| Field | Meaning |
| --- | --- |
| `mcpServers` | A map of server names to server definitions. |
| `command` | The executable that starts a local server. |
| `args` | Command arguments passed to the executable. |
| `cwd` | Working directory used when the server starts. |
| `env` | Environment variables injected into the server process. |
| `url` | Remote MCP endpoint for HTTP-style servers. |
| `transport` | Transport hint such as `stdio` or `http`. |

```json
{
  "mcpServers": {
    "filesystem": {
      "command": "definitely-missing-mcp-server",
      "args": ["server.js"],
      "cwd": "./missing-workspace",
      "env": {
        "API_KEY": "",
        "REGION": "us-central1"
      }
    }
  }
}
```

### Step 3: Run it
```bash
mcp-smoke --config examples/mcp.example.json
```

Output:

```text
mcp-smoke report
config: examples/mcp.example.json
format: mcpServers
servers: 2
findings: 4

1. [error] filesystem
   problem: command not found: definitely-missing-mcp-server
   fix: Install the command or update the config to use a valid executable name.

2. [error] filesystem
   problem: cwd does not exist: examples/missing-workspace
   fix: Create the directory or point cwd at an existing workspace.

3. [error] filesystem
   problem: script file not found: examples/server.js
   fix: Create the script file or update the first argument to an existing path.

4. [warning] preview
   problem: remote endpoint uses plain HTTP: http://example.com/mcp
   fix: Switch the endpoint to HTTPS unless it is local development.
```

### Step 4: Add to CI
Use the tool in your own pipeline after installing it in the job:

```yaml
name: smoke-check

on:
  pull_request:
  push:

jobs:
  mcp-smoke:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-go@v5
        with:
          go-version: "1.22"
      - run: go install github.com/ratelworks/mcp-smoke@latest
      - run: mcp-smoke --config .github/mcp.json
```

## How It Works
```text
config file
    |
    v
schema detection
    |
    v
normalize servers
    |
    v
filesystem and endpoint checks
    |
    v
actionable report
```

- The CLI reads one config file and supports three shapes: `mcpServers`, `servers`, and a single server object.
- It checks local command availability, working directories, scripts, and empty environment variables.
- It warns on risky remote endpoints such as plain HTTP.
- By default it never launches a server or mutates your config.
- With `--live`, it starts each stdio server, sends `initialize` and `tools/list`, and reports protocol failures.

## Feature Reference

| Feature | Description |
| --- | --- |
| Config format detection | Reads supported MCP config shapes and normalizes them into one report. |
| Local server checks | Verifies commands, working directories, env values, and script paths. |
| Remote endpoint checks | Flags invalid URLs and plain HTTP remote endpoints. |
| Live smoke test | Starts each stdio MCP server, sends `initialize` and `tools/list`, reports timeout or protocol errors. Enabled with `--live`. |
| JSON output | Returns machine-readable output for CI or automation. |

### Config Format Detection
What it does: Reads `mcpServers`, `servers`, or a single server object and turns them into one normalized list.
Example command: `mcp-smoke --config examples/mcp.example.json`
How to disable: There is no disable switch for parsing because the command needs a config file to run.

### Local Server Checks
What it does: Checks `command`, `cwd`, `env`, and script paths for local MCP servers.
Example command: `mcp-smoke --config examples/mcp.example.json`
How to disable: Add `--skip-cwd`, `--skip-env`, or `--skip-path` to turn off specific checks.

### Remote Endpoint Checks
What it does: Validates `url` values and warns when a remote endpoint uses plain HTTP.
Example command: `mcp-smoke --config examples/mcp.example.json`
How to disable: Remove the `url` field or switch to a local `stdio` server definition.

### JSON Output
What it does: Prints the report as JSON for scripts and CI jobs.
Example command: `mcp-smoke --config examples/mcp.example.json --json`
How to disable: Omit `--json` to use the text report.

## CLI Reference

| Command or Flag | Type | Description |
| --- | --- | --- |
| `mcp-smoke` | command | Runs the smoke check for one config file. |
| `--config` | flag | Path to the config file to analyze. |
| `--json` | flag | Print JSON instead of text. |
| `--skip-cwd` | flag | Skip working directory checks. |
| `--skip-env` | flag | Skip environment variable checks. |
| `--skip-path` | flag | Skip command and script path checks. |
| `--live` | flag | Start each stdio server and verify MCP handshake (initialize + tools/list). |

### Exit Codes

| Code | Meaning |
| --- | --- |
| `0` | No blocking findings were found. |
| `1` | The input is invalid or the smoke check found issues. |
| `2` | The tool hit a system error while running. |

## Development + Contributing + License
### Development
```bash
git clone https://github.com/ratelworks/mcp-smoke.git
cd mcp-smoke
make build
make test
make lint
```

### Contributing
- Open an issue before changing the core checks so the scope stays tight.
- Keep new rules deterministic and read-only.
- Add table-driven tests for every new failure mode.
- Update the README when you add a new flag or supported config shape.

### License
MIT License. See the [LICENSE](LICENSE) file for the full text.

---
<p align="center">
  Developed by <strong>RATELWORKS</strong>
</p>
