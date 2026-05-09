# kagent-sota-adapter

Generic CLI-to-A2A adapter for [kagent](https://github.com/kagent-dev/kagent). Wraps CLI-based AI coding agents (OpenAI Codex, Claude Code, etc.) as BYO agents via the A2A protocol.

## Quick Start

```python
from kagent.sota_adapter import KAgentApp
from kagent.sota_adapter.parsers import CodexEventParser

app = KAgentApp(
    parser=CodexEventParser(),
    agent_card=agent_card,
    config=config,
)
fastapi_app = app.build()
```

## Adding a New CLI Agent

1. Implement `EventParser` (see `parsers/_codex.py` for reference)
2. Create a sample under `samples/sota/`
3. Build a container image and deploy as a BYO Agent CRD
