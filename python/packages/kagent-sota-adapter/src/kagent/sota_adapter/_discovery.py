"""LLM endpoint auto-discovery for CLI agent runtimes.

Resolves LLM connectivity by probing environment variables and Kubernetes DNS,
so users don't need to manually configure proxy URLs and API keys.

Resolution order:
1. OPENAI_BASE_URL env var set -> use directly
2. sap-ai-proxy service in KAGENT_NAMESPACE -> proxy path
3. SAP_AI_CORE_* env vars complete -> direct AI Core with token refresh
4. OPENAI_API_KEY set -> direct OpenAI
5. Raise RuntimeError
"""

from __future__ import annotations

import logging
import os
import socket
from collections.abc import Callable
from dataclasses import dataclass

logger = logging.getLogger(__name__)

_PROXY_SERVICE_NAME = "sap-ai-proxy"
_PROXY_PORT = 3030
_PROXY_PATH = "/v1"
_PLACEHOLDER_API_KEY = "placeholder"


@dataclass(frozen=True)
class DiscoveredEndpoint:
    """Result of LLM endpoint auto-discovery."""

    base_url: str
    api_key: str
    source: str  # "explicit", "proxy", "sap_ai_core", "openai"
    needs_token_refresh: bool = False
    sap_auth_url: str | None = None
    sap_client_id: str | None = None
    sap_client_secret: str | None = None


def _dns_resolve(host: str, port: int, timeout: float = 2.0) -> bool:
    """Check if a hostname resolves and the port is connectable."""
    try:
        addrs = socket.getaddrinfo(host, port, socket.AF_INET, socket.SOCK_STREAM)
        if not addrs:
            return False
        sock = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
        sock.settimeout(timeout)
        try:
            sock.connect(addrs[0][4])
            return True
        except (ConnectionRefusedError, TimeoutError, OSError):
            return False
        finally:
            sock.close()
    except socket.gaierror:
        return False


def discover_endpoint() -> DiscoveredEndpoint:
    """Auto-discover the LLM endpoint using environment and Kubernetes DNS.

    Returns:
        DiscoveredEndpoint with the resolved base_url, api_key, and source.

    Raises:
        RuntimeError: If no usable endpoint is discoverable.
    """
    # 1. Explicit override
    openai_base_url = os.getenv("OPENAI_BASE_URL")
    if openai_base_url:
        api_key = os.getenv("OPENAI_API_KEY", _PLACEHOLDER_API_KEY)
        logger.info("Using explicit OPENAI_BASE_URL: %s", openai_base_url)
        return DiscoveredEndpoint(base_url=openai_base_url, api_key=api_key, source="explicit")

    # 2. DNS probe for sap-ai-proxy in the same namespace
    namespace = os.getenv("KAGENT_NAMESPACE")
    if namespace:
        proxy_host = f"{_PROXY_SERVICE_NAME}.{namespace}.svc.cluster.local"
        if _dns_resolve(proxy_host, _PROXY_PORT):
            base_url = f"http://{_PROXY_SERVICE_NAME}.{namespace}:{_PROXY_PORT}{_PROXY_PATH}"
            logger.info("Discovered sap-ai-proxy at %s", base_url)
            return DiscoveredEndpoint(base_url=base_url, api_key=_PLACEHOLDER_API_KEY, source="proxy")
        else:
            logger.debug("sap-ai-proxy not reachable at %s:%d", proxy_host, _PROXY_PORT)

    # 3. Direct SAP AI Core
    sap_base = os.getenv("SAP_AI_CORE_BASE_URL")
    sap_auth = os.getenv("SAP_AI_CORE_AUTH_URL")
    sap_id = os.getenv("SAP_AI_CORE_CLIENT_ID")
    sap_secret = os.getenv("SAP_AI_CORE_CLIENT_SECRET")
    sap_deploy = os.getenv("SAP_AI_CORE_DEPLOY_ID")
    if all([sap_base, sap_auth, sap_id, sap_secret, sap_deploy]):
        deploy_url = f"{sap_base.rstrip('/')}/v2/inference/deployments/{sap_deploy}/v1"
        logger.info("Using SAP AI Core endpoint: %s", deploy_url)
        return DiscoveredEndpoint(
            base_url=deploy_url,
            api_key="",
            source="sap_ai_core",
            needs_token_refresh=True,
            sap_auth_url=sap_auth,
            sap_client_id=sap_id,
            sap_client_secret=sap_secret,
        )

    # 4. Plain OpenAI
    api_key = os.getenv("OPENAI_API_KEY")
    if api_key:
        logger.info("Using default OpenAI endpoint")
        return DiscoveredEndpoint(base_url="https://api.openai.com/v1", api_key=api_key, source="openai")

    raise RuntimeError(
        "No LLM endpoint discoverable. Set OPENAI_BASE_URL, deploy sap-ai-proxy, "
        "provide SAP_AI_CORE_* env vars, or set OPENAI_API_KEY."
    )


def build_executor_config(
    endpoint: DiscoveredEndpoint,
    runtime: str,
    model: str,
) -> "CLIAgentExecutorConfig":
    """Build a CLIAgentExecutorConfig from a discovered endpoint.

    Args:
        endpoint: The discovered LLM endpoint.
        runtime: "codex" or "claude-code".
        model: Model name (e.g., "gpt-5", "claude-sonnet-4-20250514").

    Returns:
        A fully-configured CLIAgentExecutorConfig.
    """
    from ._executor import CLIAgentExecutorConfig

    if runtime == "codex":
        extra_args, env_vars, pre_execute = _build_codex_args(endpoint, model)
    elif runtime == "claude-code":
        extra_args, env_vars, pre_execute = _build_claude_code_args(endpoint, model)
    else:
        raise ValueError(f"Unknown runtime: {runtime!r}. Supported: codex, claude-code")

    return CLIAgentExecutorConfig(
        extra_args=extra_args,
        env_vars=env_vars,
        pre_execute=pre_execute,
    )


def _build_codex_args(
    endpoint: DiscoveredEndpoint,
    model: str,
) -> tuple[list[str], dict[str, str], Callable[[], dict[str, str]] | None]:
    """Build Codex CLI args from a discovered endpoint."""
    common_args = ["-c", 'web_search="disabled"', "-m", model]
    env_vars: dict[str, str] = {}
    pre_execute = None

    if endpoint.source in ("proxy", "explicit"):
        extra_args = [
            "-c", 'model_provider="proxy"',
            "-c", 'model_providers.proxy.name="OpenAI Proxy"',
            "-c", f'model_providers.proxy.base_url="{endpoint.base_url}"',
            "-c", 'model_providers.proxy.env_key="OPENAI_API_KEY"',
            *common_args,
        ]
        env_vars["OPENAI_API_KEY"] = endpoint.api_key

    elif endpoint.source == "sap_ai_core":
        from kagent.adk.models._sap_ai_core import _get_oauth_token_sync

        auth_url = endpoint.sap_auth_url
        client_id = endpoint.sap_client_id
        client_secret = endpoint.sap_client_secret

        def refresh_token() -> dict[str, str]:
            token, _ = _get_oauth_token_sync(auth_url, client_id, client_secret)
            return {"OPENAI_API_KEY": token}

        refresh_token()  # validate at startup
        logger.info("SAP AI Core OAuth token fetched successfully")

        extra_args = [
            "-c", 'model_provider="sapaicore"',
            "-c", 'model_providers.sapaicore.name="SAP AI Core"',
            "-c", f'model_providers.sapaicore.base_url="{endpoint.base_url}"',
            "-c", 'model_providers.sapaicore.env_key="OPENAI_API_KEY"',
            *common_args,
        ]
        pre_execute = refresh_token

    else:  # openai
        extra_args = [*common_args]
        env_vars["OPENAI_API_KEY"] = endpoint.api_key

    return extra_args, env_vars, pre_execute


def _build_claude_code_args(
    endpoint: DiscoveredEndpoint,
    model: str,
) -> tuple[list[str], dict[str, str], Callable[[], dict[str, str]] | None]:
    """Build Claude Code CLI args from a discovered endpoint."""
    extra_args = ["--model", model]
    env_vars: dict[str, str] = {}
    pre_execute = None

    if endpoint.source in ("proxy", "explicit"):
        env_vars["ANTHROPIC_BASE_URL"] = endpoint.base_url
        env_vars["ANTHROPIC_API_KEY"] = endpoint.api_key

    elif endpoint.source == "sap_ai_core":
        from kagent.adk.models._sap_ai_core import _get_oauth_token_sync

        auth_url = endpoint.sap_auth_url
        client_id = endpoint.sap_client_id
        client_secret = endpoint.sap_client_secret

        def refresh_token() -> dict[str, str]:
            token, _ = _get_oauth_token_sync(auth_url, client_id, client_secret)
            return {"ANTHROPIC_API_KEY": token}

        refresh_token()  # validate at startup
        logger.info("SAP AI Core OAuth token fetched successfully")
        env_vars["ANTHROPIC_BASE_URL"] = endpoint.base_url
        pre_execute = refresh_token

    else:  # openai / anthropic direct
        api_key = os.getenv("ANTHROPIC_API_KEY", endpoint.api_key)
        env_vars["ANTHROPIC_API_KEY"] = api_key

    return extra_args, env_vars, pre_execute
