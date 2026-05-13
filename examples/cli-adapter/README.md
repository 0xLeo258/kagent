# CLI-A2A-Adapter Examples

Deploy CLI-based AI agents (Claude Code, Codex, or any custom CLI) in kagent using the [CLI-A2A-Adapter](https://github.com/0xLeo258/CLI-A2A-Adapter).

## Prerequisites

- A running kagent cluster
- The CLI-A2A-Adapter image available (`ghcr.io/0xleo258/cli-a2a-adapter:latest`)

## Usage

### 1. Create secrets and model config

```bash
kubectl apply -f model-config.yaml
```

Edit the secret values first (replace `REPLACE_ME` with actual API keys).

### 2. Deploy an agent

```bash
# Claude Code
kubectl apply -f claude-agent.yaml

# Codex
kubectl apply -f codex-agent.yaml

# Generic CLI
kubectl apply -f generic-agent.yaml
```

### 3. Verify

```bash
# Check agent is running
kubectl get agents

# Port-forward to test A2A endpoint directly
kubectl port-forward svc/claude-code 8080:8080
curl -s http://localhost:8080/.well-known/agent.json | jq .
```

## How it works

The CLI-A2A-Adapter is a Go binary that:
1. Starts an A2A JSON-RPC server on port 8080
2. Receives messages via the A2A protocol
3. Spawns a CLI subprocess (claude/codex/custom) per request
4. Streams CLI output back as A2A events

Since it exposes the standard A2A protocol on `:8080`, kagent treats it like any other agent — routing, session management, and UI all work out of the box.

## Architecture

```
kagent control plane
    │ A2A JSON-RPC
    ▼
┌───────────────────────────────┐
│ CLI-A2A-Adapter Pod           │
│   :8080 A2A Server            │
│   └─ Backend (claude/codex)   │
│       └─ CLI subprocess       │
└───────────────────────────────┘
```
