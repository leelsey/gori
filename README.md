# gori

A lightweight, embeddable **LLM agent framework** in Go — zero third-party
dependencies, fast, and usable as a library, a CLI, or an MCP / A2A server.

> Personal project under **Leelsey**. Licensed under the [MIT Licence](LICENSE).

## Why

- **Embeddable first.** Import `github.com/leelsey/gori` and drive agents from
  your own Go program; the CLI is just a thin wrapper around the same library.
- **Zero dependencies.** The whole framework — HTTP clients, SSE parsing, agent
  loop, config — is pure Go standard library. `go.mod` has no `require` block.
- **Multi-provider.** OpenAI, Anthropic and Google are supported through one
  neutral interface, plus a generic adapter that shells out to agentic CLIs
  (`codex`, `claude`, `agy`).

## Requirements

- Go 1.26 or newer

## Install

```sh
# As a library
go get github.com/leelsey/gori

# The CLI
go install github.com/leelsey/gori/cmd/gori@latest
```

## Library usage

```go
package main

import (
	"context"
	"fmt"
	"os"

	"github.com/leelsey/gori"
	"github.com/leelsey/gori/provider/anthropic"
)

func main() {
	agent := &gori.Agent{
		Provider: anthropic.New(os.Getenv("ANTHROPIC_API_KEY")),
		Model:    "claude-sonnet-4-6",
		System:   "You are concise.",
		Session:  gori.NewSession(),
	}
	out, _ := agent.Run(context.Background(), "Say hello in one word.")
	fmt.Println(out.Text())
}
```

Register tools and the agent will run the reason-act-observe (ReAct) loop,
executing tools until the model produces a final answer — see
[`examples/embed`](examples/embed/main.go).

Stream tokens as they arrive:

```go
agent.Stream(ctx, "Tell me a joke", func(ev gori.StreamEvent) error {
	if ev.Type == gori.EventTextDelta {
		fmt.Print(ev.Text)
	}
	return nil
})
```

## CLI usage

```sh
# Ad-hoc: provider + model from flags, key from the standard env var
ANTHROPIC_API_KEY=... gori --model claude-sonnet-4-6 "Explain goroutines briefly"
OPENAI_API_KEY=...    gori --provider openai --model gpt-5.2 "Explain goroutines"
GEMINI_API_KEY=...    gori --provider google --model gemini-2.5-flash "Explain goroutines"

# External agentic CLI as the brain — no API key, no config (uses your CLI login)
gori --cli "claude -p --model haiku" "Explain goroutines briefly"
echo "summarise this" | gori --cli "claude -p"

# Local / custom OpenAI-compatible server (Ollama, vLLM) — no config file
gori --provider openai --base-url http://localhost:11434/v1 --model llama3.1 "Explain goroutines"
# Custom key from a named env var (value stays out of argv and shell history)
export VLLM_TOKEN=...   # set from a file / secret manager, not typed literally
gori --provider openai --base-url http://localhost:8000/v1 --api-key-env VLLM_TOKEN --model my-model "hi"

# Piped input
echo "summarise this" | gori --model claude-sonnet-4-6

# Config-driven personas
gori --config examples/gori.json --agent main "Plan a release"

# Flags
gori --think auto --model claude-sonnet-4-6 "Solve this step by step"
gori --no-stream --model claude-sonnet-4-6 "..."
gori --timeout 90s --model claude-sonnet-4-6 "..."   # overall deadline (0: none)
gori --usage --model claude-sonnet-4-6 "..."         # print token usage to stderr
gori --debug --model claude-sonnet-4-6 "..."         # dump HTTP traffic (keys redacted)
gori --version

# Multimodal input (attach files; repeatable)
gori --provider openai --model gpt-5.2 --image photo.png "What is in this image?"
gori --provider google --model gemini-2.5-flash --audio clip.wav "Transcribe this"
# Request non-text OUTPUT with --modality (the provider must support it); any
# returned image/audio is saved to gori-*.* files.
gori --provider openai --model gpt-4o-audio-preview --modality audio "Say hello aloud"

# Interactive terminal session (TUI), and help
gori tui --config gori.json --agent main    # /usage shows token usage, /help lists commands
gori tui --usage -m claude-sonnet-4-6       # print usage after each turn
gori tui --debug -m claude-sonnet-4-6       # wire dump; /debug toggles a per-turn step/tool trace
gori --help
```

Flags accept GNU-style `--flag`; single-dash (`-flag`) is still honoured, and
`-m` is shorthand for `--model`.

Default API-key env vars in ad-hoc mode: `ANTHROPIC_API_KEY`, `OPENAI_API_KEY`,
`GEMINI_API_KEY`.

## Configuration (JSON)

Providers and agent personas are declared in JSON (see
[`examples/gori.json`](examples/gori.json)). API keys are referenced by env-var
**name** — secrets are never stored in the config file. CLI backends wrap an
external agentic tool:

```json
{
  "name": "claude-cli",
  "type": "cli",
  "command": "claude",
  "args": ["-p"],
  "prompt_via": "stdin"
}
```

**API keys never live in the file.** A provider references either an env-var
name (`api_key_env`) or a command that prints the key (`api_key_cmd`, e.g. a
keychain / secret-manager lookup), so the value stays out of the config, argv,
and shell history:

```json
{ "name": "claude", "type": "anthropic",
  "api_key_cmd": "security find-generic-password -s gori-anthropic -w" }
```

### Managing config from the CLI

When `--config` is omitted, gori searches `$GORI_CONFIG`, then `./gori.json`,
then `~/.config/gori/config.json`. Manage that file without hand-editing JSON:

```sh
gori config init                       # create a starter config at the default path
gori config add-provider --name claude --type anthropic --api-key-env ANTHROPIC_API_KEY
gori config add-provider --name local  --type openai --base-url http://localhost:11434/v1 --api-key-env OLLAMA_KEY
gori config add-agent --name main --provider claude --model claude-sonnet-4-6 --system "Be concise." --default
gori config set-default main
gori config show | validate | path     # inspect / verify / locate
gori config rm-agent main              # removals are validation-guarded
gori config rm-provider local
gori config edit                       # open $EDITOR; validates on save, refuses if broken
```

Every mutation re-validates referential integrity before writing (0600), and
refuses to save a broken config. With a config in place you can drop `--config`:
`gori "prompt"` uses the discovered file's `default_agent`.

## Multi-agent orchestration

A main agent can delegate subtasks to named sub-agents, which run concurrently.
Sub-agents are exposed to the main model as tools; an in-process event `Bus`
lets you observe every agent.

```go
bus := gori.NewBus()
o := gori.NewOrchestrator(bus)
o.Add("main", "main", mainAgent)
o.Add("researcher", "sub", researchAgent)
o.WireDelegation(map[string]string{
	"researcher": "Delegate factual research questions to this agent.",
})
out, _ := o.Run(ctx, "Research X and summarise.")
```

From the CLI, drive a config-defined team (agent `role` + `description`):

```sh
gori --orchestrate --config examples/gori.json "Plan and research a release"
```

## MCP (Model Context Protocol)

Gori speaks MCP over stdio in both directions.

**As a server** — expose your configured agents to any MCP client (Claude
Desktop, IDEs, inspectors). stdout carries the protocol; logs go to stderr:

```sh
gori mcp-server --config gori.json
```

**As a client** — consume an external MCP server's tools from your agents by
listing them under `mcp_servers`:

```json
"mcp_servers": [
  { "name": "fs", "command": "mcp-server-filesystem", "args": ["/data"] }
]
```

Library API: `mcp.NewServer(name, ver).AddRegistry(...)` / `.AddAgent(...)` to
serve; `mcp.Dial(ctx, cmd, args...)` then `client.Tools(ctx)` returns
`[]gori.Tool` ready to register on an agent.

## A2A (Agent2Agent)

Gori speaks A2A over its JSON-RPC/HTTP binding, both ways.

**As a server** — expose an agent as an A2A agent (Agent Card + `message/send` +
SSE streaming + tasks):

```sh
gori a2a-serve --config gori.json --agent main --addr :8080
# card: http://localhost:8080/.well-known/agent-card.json
```

**As a client** — call remote A2A agents from your agents by listing them under
`a2a_agents`:

```json
"a2a_agents": [
  { "name": "researcher", "url": "http://other-host:8080", "description": "deep research" }
]
```

Library API: `a2a.NewServer(card, a2a.AgentHandler(agent)).HTTPHandler()`;
`a2a.NewClient(url)` with `.SendMessage` / `.SendMessageStream` / `.AsTool`.

**gRPC binding** (`a2a/grpc`, generated from the official `a2a.proto`): same
semantics over gRPC — `grpca2a.Serve(ctx, addr, a2a.AgentHandler(agent))` and
`grpca2a.Dial(addr)` with `.SendMessage` / `.SendMessageStream` / `.AsTool`.
It is the **only** package that pulls grpc/protobuf — the core, MCP, network
bus, HTTP A2A, and the default `gori` binary stay dependency-free.

## Network bus

Bridge the in-process event `Bus` across processes and machines through a small
central HTTP+SSE hub:

```sh
gori bus --addr :7777          # run the hub
```

Point Gori instances at it via config (`"bus": "http://hub-host:7777"`): each
process's local `Bus` events are published to the hub and remote events injected
back, with origin tagging to prevent loops. Pure stdlib (no new dependencies).
Library: `netbus.NewHub().Handler()` and
`netbus.NewClient(url).Bridge(ctx, bus, topics...)`.

## Resilience & lifecycle

Built for long-running embeds and real-world network faults — all standard library:

- **Automatic retries (on by default).** Provider HTTP calls retry transient
  failures (HTTP 429, 5xx, network errors) with exponential backoff + jitter,
  honouring `Retry-After`. Tune or disable per provider with
  [`httpretry`](httpretry/httpretry.go):
  `anthropic.New(key).WithRetry(httpretry.Policy{Attempts: 5})` or `.WithoutRetry()`.
- **Deadlines.** Every request honours its `context`. The CLI's `--timeout` wraps
  the run in a deadline; library callers pass a `context.WithTimeout` or a custom
  `*http.Client` via `WithHTTPClient`. No client-level timeout is set by default,
  so streaming responses are never cut mid-stream.
- **Graceful shutdown.** `gori a2a-serve` and `gori bus` drain in-flight requests
  on SIGINT/SIGTERM (read/idle timeouts set; no write timeout, so SSE survives).
- **A2A task lifecycle.** The server evicts old/terminal tasks (TTL + size cap),
  and `tasks/cancel` cancels the in-flight handler's context.
- **Self-healing bus bridge.** `netbus` clients reconnect with backoff and resume
  from the last event ID (`Last-Event-ID`) after a hub blip.
- **Bounded teardown.** `mcp.Client.Close()` kills a child that ignores stdin EOF
  rather than hanging.
- **Token accounting & observability.** `Agent.TotalUsage` reports cumulative
  tokens for the last run, `Agent.SessionUsage` across all runs, and
  `Agent.StepUsage` per provider call; `Usage` carries input/output/thinking
  plus cache read/write token counts (cached tokens are a breakdown of input).
  `Orchestrator.Usage()` aggregates delegated sub-agent usage; the CLI prints
  it all with `--usage` (TUI: `/usage`); `Bus.Dropped()` surfaces events
  dropped to a slow consumer.
- **Debugging.** `httpdebug.NewClient(w)` (or `--debug` on the CLI/TUI) dumps
  every provider HTTP request/response — headers, bodies, live SSE, retries —
  with credentials redacted; wire it into any provider via `WithHTTPClient`.
  `Agent.Bus` publishes typed lifecycle events (`StepEvent` with stop reason
  and usage per provider call, `ToolCallEvent`/`ToolResultEvent` with
  arguments and results) shown by `--orchestrate`'s event log and the TUI's
  `/debug` per-turn trace.
- **History compaction.** `Session.Trim(keepLast)` / `Session.DropBefore(n)` cap
  unbounded conversation growth and token cost.

## Project layout

```
gori/
├── gori.go provider.go agent.go message.go content.go tool.go session.go config.go
│   bus.go orchestrator.go   # root package `gori`: public API, ReAct loop, orchestration
├── provider/
│   ├── anthropic/  openai/  google/   # hand-rolled net/http adapters
│   └── clibackend/                    # generic agentic-CLI adapter (os/exec)
├── httpretry/            # retry/backoff policy (public, stdlib)
├── mcp/  a2a/  a2a/grpc/  netbus/     # interop: MCP, A2A (HTTP + gRPC), network bus
├── internal/
│   ├── sse/  rpc/  jsonrpc/   # SSE parser + JSON-RPC transport/types
│   ├── build/                # constructs providers/agents from config
│   └── tui/                  # interactive terminal session (binary-only)
├── cmd/gori/              # thin CLI binary + config subcommands
└── examples/embed/        # embedding gori as a library
```

## Build & test

```sh
go build ./...
go vet ./...
go test ./...
go test -tags live -run TestLive ./...   # live integration: needs the `claude` CLI

make build      # version-stamped binary -> bin/gori
make release    # cross-compiled dist/gori-<os>-<arch>[.exe] + checksums.txt
```

Provider clients are tested against `httptest` servers, so the default suite runs
fully offline. The core, `mcp`, `netbus` and the JSON-RPC/HTTP `a2a` pull no
third-party dependencies; only `a2a/grpc` adds gRPC/protobuf, and it is not linked
into the default `gori` binary.

## Notes & limitations (v0.1)

- OpenAI uses the **Chat Completions** API (stable, well-understood). `reasoning_effort`
  and `temperature` are gated by model family — reasoning models (o1/o3/o4) get the
  former, others the latter — so neither triggers a 400. Reasoning *text* is not
  surfaced by this endpoint; the Responses API is a future enhancement.
- **Multimodal.** Image + audio **input** work across providers (`--image` /
  `--audio`, or `Agent.RunMessage`). Media **output** returned by a model is parsed
  and saved to `gori-*.*`; request it with `--modality` / `Agent.ResponseModalities`
  where the provider supports it. **Anthropic has no audio input** — the CLI warns
  and the attachment is ignored.
- Multi-agent orchestration, MCP (stdio), A2A (HTTP + gRPC) and the network bus are
  all available.

## Licence

MIT © 2026 Leelsey
