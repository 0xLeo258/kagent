# Codex Agent Sample

BYO agent wrapping OpenAI Codex CLI via `kagent-sota-adapter`.

## Prerequisites

- [Codex CLI](https://github.com/openai/codex) installed (`codex` in PATH)
- `OPENAI_API_KEY` environment variable set

## Local Run

```bash
cd python
uv run python samples/sota/codex_agent/codex_agent/agent.py
```

## Deploy to Kind

```bash
kubectl apply -f samples/sota/codex_agent/agent.yaml
```
