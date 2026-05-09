"""Generic CLI agent runtime entrypoint.

Supports running Codex or Claude Code agents with auto-discovered LLM endpoints.
Start with: python -m kagent.sota_adapter

Environment variables:
    CLI_RUNTIME      - "codex" or "claude-code" (auto-detected from PATH if absent)
    CLI_MODEL        - Model name (default: runtime-specific)
    KAGENT_NAME      - Agent name (injected by controller)
    KAGENT_NAMESPACE - Pod namespace (injected by controller)
    KAGENT_URL       - Backend URL (injected by controller)
    OPENAI_BASE_URL  - Optional explicit LLM endpoint override
    OPENAI_API_KEY   - Optional API key override
"""

from __future__ import annotations

import logging
import os
import shutil
import sys

import uvicorn
from a2a.types import AgentCard
from kagent.core import KAgentConfig

from ._a2a import KAgentApp
from ._discovery import build_executor_config, discover_endpoint
from .parsers._claude_code import ClaudeCodeEventParser
from .parsers._codex import CodexEventParser

logger = logging.getLogger(__name__)

# runtime name -> (parser_class, default_model, binary_name)
_RUNTIMES: dict[str, tuple[type, str, str]] = {
    "codex": (CodexEventParser, "gpt-5", "codex"),
    "claude-code": (ClaudeCodeEventParser, "claude-sonnet-4-20250514", "claude"),
}


def _detect_runtime() -> str:
    """Detect runtime from CLI_RUNTIME env or by probing PATH for known binaries."""
    explicit = os.getenv("CLI_RUNTIME", "").strip().lower()
    if explicit:
        if explicit not in _RUNTIMES:
            logger.error("CLI_RUNTIME=%s not supported. Options: %s", explicit, list(_RUNTIMES))
            sys.exit(1)
        return explicit

    for name, (_, _, binary) in _RUNTIMES.items():
        if shutil.which(binary):
            logger.info("Auto-detected runtime: %s (found '%s' on PATH)", name, binary)
            return name

    logger.error("No CLI_RUNTIME set and no known CLI binary found on PATH")
    sys.exit(1)


def main() -> None:
    logging.basicConfig(level=logging.INFO, format="%(asctime)s %(levelname)s [%(name)s] %(message)s")

    runtime_name = _detect_runtime()
    parser_cls, default_model, _ = _RUNTIMES[runtime_name]

    model = os.getenv("CLI_MODEL", default_model)
    port = int(os.getenv("PORT", "8080"))

    endpoint = discover_endpoint()
    logger.info("Runtime=%s, model=%s, endpoint=%s (%s)", runtime_name, model, endpoint.base_url, endpoint.source)

    executor_config = build_executor_config(endpoint, runtime_name, model)

    parser = parser_cls()
    defaults = parser.get_agent_card_defaults()

    agent_name = os.getenv("KAGENT_NAME", defaults["name"])
    agent_card = AgentCard(
        name=agent_name,
        description=defaults.get("description", f"{runtime_name} agent"),
        url=f"localhost:{port}",
        version=defaults.get("version", "1.0.0"),
        capabilities={"streaming": True},
        defaultInputModes=["text"],
        defaultOutputModes=["text"],
        skills=defaults.get("skills", []),
    )

    config = KAgentConfig()

    app = KAgentApp(
        parser=parser,
        agent_card=agent_card,
        config=config,
        executor_config=executor_config,
    )

    fastapi_app = app.build()

    logger.info("Starting %s runtime (model=%s) on port %d", runtime_name, model, port)
    uvicorn.run(fastapi_app, host="0.0.0.0", port=port)


if __name__ == "__main__":
    main()
