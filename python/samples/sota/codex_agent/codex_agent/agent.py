"""Codex Agent — BYO agent wrapping OpenAI Codex CLI via kagent-sota-adapter.

Usage:
    python agent.py                     # Start A2A server on :8080
    curl http://localhost:8080/health   # Health check
"""

import logging
import os

from a2a.types import AgentCard
from kagent.core import KAgentConfig
from kagent.sota_adapter import CLIAgentExecutorConfig, KAgentApp
from kagent.sota_adapter.parsers import CodexEventParser

logger = logging.getLogger(__name__)

# Create the event parser for Codex CLI
parser = CodexEventParser()

# Agent card for A2A protocol
agent_card = AgentCard(
    name="codex-agent",
    description="OpenAI Codex coding agent — generates, edits, and refactors code",
    url="localhost:8080",
    version="1.0.0",
    capabilities={"streaming": True},
    defaultInputModes=["text"],
    defaultOutputModes=["text"],
    skills=[
        {
            "id": "coding",
            "name": "Code Generation & Editing",
            "description": "Generate, edit, and refactor code using OpenAI Codex CLI",
            "tags": ["code", "codex", "openai"],
        }
    ],
)

config = KAgentConfig()


def _build_executor_config() -> CLIAgentExecutorConfig:
    """Build executor config, auto-detecting proxy or SAP AI Core env vars."""
    codex_model = os.getenv("CODEX_MODEL", "gpt-5")

    # Common args: disable web_search (not supported by SAP AI Core)
    common_args = ["-c", 'web_search="disabled"', "-m", codex_model]

    # Path 1: OpenAI-compatible proxy (e.g. sap-ai-proxy) — simplest
    openai_base_url = os.getenv("OPENAI_BASE_URL")
    if openai_base_url:
        logger.info(f"Configuring Codex CLI to use proxy: {openai_base_url}")
        return CLIAgentExecutorConfig(
            extra_args=[
                "-c", 'model_provider="proxy"',
                "-c", 'model_providers.proxy.name="OpenAI Proxy"',
                "-c", f'model_providers.proxy.base_url="{openai_base_url}"',
                "-c", 'model_providers.proxy.env_key="OPENAI_API_KEY"',
                *common_args,
            ],
        )

    # Path 2: Direct SAP AI Core (OAuth token refresh per request)
    sap_base_url = os.getenv("SAP_AI_CORE_BASE_URL")
    sap_auth_url = os.getenv("SAP_AI_CORE_AUTH_URL")
    sap_client_id = os.getenv("SAP_AI_CORE_CLIENT_ID")
    sap_client_secret = os.getenv("SAP_AI_CORE_CLIENT_SECRET")
    sap_deploy_id = os.getenv("SAP_AI_CORE_DEPLOY_ID")

    if sap_base_url and sap_auth_url and sap_client_id and sap_client_secret and sap_deploy_id:
        deploy_url = f"{sap_base_url.rstrip('/')}/v2/inference/deployments/{sap_deploy_id}/v1"
        logger.info(f"Configuring Codex CLI to use SAP AI Core: {deploy_url}")

        from kagent.adk.models._sap_ai_core import _get_oauth_token_sync

        def refresh_token() -> dict[str, str]:
            """Fetch/refresh SAP AI Core OAuth token (cached, thread-safe)."""
            token, _ = _get_oauth_token_sync(sap_auth_url, sap_client_id, sap_client_secret)
            return {"OPENAI_API_KEY": token}

        try:
            refresh_token()
            logger.info("SAP AI Core OAuth token fetched successfully")
        except Exception as e:
            logger.error(f"Failed to fetch SAP AI Core token: {e}")
            raise

        return CLIAgentExecutorConfig(
            extra_args=[
                "-c", 'model_provider="sapaicore"',
                "-c", 'model_providers.sapaicore.name="SAP AI Core"',
                "-c", f'model_providers.sapaicore.base_url="{deploy_url}"',
                "-c", 'model_providers.sapaicore.env_key="OPENAI_API_KEY"',
                *common_args,
            ],
            pre_execute=refresh_token,
        )

    # Path 3: Direct OpenAI (fallback)
    logger.info("No proxy or SAP AI Core env vars set, using default OpenAI endpoint")
    return CLIAgentExecutorConfig(extra_args=common_args)


executor_config = _build_executor_config()

# Create KAgent app
app = KAgentApp(
    parser=parser,
    agent_card=agent_card,
    config=config,
    executor_config=executor_config,
)

# Build the FastAPI application
fastapi_app = app.build()

if __name__ == "__main__":
    import uvicorn

    logging.basicConfig(level=logging.INFO)
    logger.info("Starting Codex Agent...")
    logger.info("Server will be available at http://0.0.0.0:8080")

    uvicorn.run(fastapi_app, host="0.0.0.0", port=8080)
