"""Tests for the auto-discovery module."""

import os
from unittest.mock import patch

import pytest

from kagent.sota_adapter._discovery import (
    DiscoveredEndpoint,
    _dns_resolve,
    build_executor_config,
    discover_endpoint,
)


class TestDiscoverEndpointExplicit:
    def test_openai_base_url_takes_precedence(self):
        env = {"OPENAI_BASE_URL": "http://my-proxy:3030/v1", "OPENAI_API_KEY": "sk-test"}
        with patch.dict(os.environ, env, clear=True):
            ep = discover_endpoint()
        assert ep.source == "explicit"
        assert ep.base_url == "http://my-proxy:3030/v1"
        assert ep.api_key == "sk-test"

    def test_openai_base_url_without_key_uses_placeholder(self):
        env = {"OPENAI_BASE_URL": "http://proxy:3030/v1"}
        with patch.dict(os.environ, env, clear=True):
            ep = discover_endpoint()
        assert ep.source == "explicit"
        assert ep.api_key == "placeholder"


class TestDiscoverEndpointProxy:
    def test_proxy_discovered_via_dns(self):
        env = {"KAGENT_NAMESPACE": "my-ns"}
        with (
            patch.dict(os.environ, env, clear=True),
            patch("kagent.sota_adapter._discovery._dns_resolve", return_value=True),
        ):
            ep = discover_endpoint()
        assert ep.source == "proxy"
        assert "sap-ai-proxy.my-ns" in ep.base_url
        assert ep.api_key == "placeholder"

    def test_proxy_not_found_falls_through(self):
        env = {"KAGENT_NAMESPACE": "my-ns", "OPENAI_API_KEY": "sk-fallback"}
        with (
            patch.dict(os.environ, env, clear=True),
            patch("kagent.sota_adapter._discovery._dns_resolve", return_value=False),
        ):
            ep = discover_endpoint()
        assert ep.source == "openai"


class TestDiscoverEndpointSapAiCore:
    def test_sap_ai_core_direct(self):
        env = {
            "SAP_AI_CORE_BASE_URL": "https://api.ai.example.com",
            "SAP_AI_CORE_AUTH_URL": "https://auth.example.com",
            "SAP_AI_CORE_CLIENT_ID": "client-id",
            "SAP_AI_CORE_CLIENT_SECRET": "client-secret",
            "SAP_AI_CORE_DEPLOY_ID": "deploy-123",
        }
        with patch.dict(os.environ, env, clear=True):
            ep = discover_endpoint()
        assert ep.source == "sap_ai_core"
        assert ep.needs_token_refresh
        assert "deploy-123" in ep.base_url
        assert ep.sap_auth_url == "https://auth.example.com"

    def test_incomplete_sap_ai_core_falls_through(self):
        env = {
            "SAP_AI_CORE_BASE_URL": "https://api.ai.example.com",
            # Missing other required vars
            "OPENAI_API_KEY": "sk-fallback",
        }
        with patch.dict(os.environ, env, clear=True):
            ep = discover_endpoint()
        assert ep.source == "openai"


class TestDiscoverEndpointOpenAI:
    def test_openai_fallback(self):
        env = {"OPENAI_API_KEY": "sk-test"}
        with patch.dict(os.environ, env, clear=True):
            ep = discover_endpoint()
        assert ep.source == "openai"
        assert ep.base_url == "https://api.openai.com/v1"
        assert ep.api_key == "sk-test"


class TestDiscoverEndpointNoEndpoint:
    def test_raises_when_nothing_available(self):
        with patch.dict(os.environ, {}, clear=True):
            with pytest.raises(RuntimeError, match="No LLM endpoint discoverable"):
                discover_endpoint()


class TestBuildExecutorConfigCodex:
    def test_proxy_path(self):
        ep = DiscoveredEndpoint(base_url="http://proxy:3030/v1", api_key="placeholder", source="proxy")
        config = build_executor_config(ep, "codex", "gpt-5")
        assert any("proxy" in arg for arg in config.extra_args)
        assert any("gpt-5" in arg for arg in config.extra_args)
        assert config.env_vars.get("OPENAI_API_KEY") == "placeholder"
        assert config.pre_execute is None

    def test_openai_path(self):
        ep = DiscoveredEndpoint(base_url="https://api.openai.com/v1", api_key="sk-test", source="openai")
        config = build_executor_config(ep, "codex", "gpt-5")
        assert any("gpt-5" in arg for arg in config.extra_args)
        assert config.env_vars.get("OPENAI_API_KEY") == "sk-test"


class TestBuildExecutorConfigClaudeCode:
    def test_proxy_path(self):
        ep = DiscoveredEndpoint(base_url="http://proxy:3030/v1", api_key="placeholder", source="proxy")
        config = build_executor_config(ep, "claude-code", "claude-sonnet-4-20250514")
        assert "--model" in config.extra_args
        assert "claude-sonnet-4-20250514" in config.extra_args
        assert config.env_vars.get("ANTHROPIC_BASE_URL") == "http://proxy:3030/v1"
        assert config.env_vars.get("ANTHROPIC_API_KEY") == "placeholder"

    def test_unknown_runtime_raises(self):
        ep = DiscoveredEndpoint(base_url="http://x", api_key="k", source="proxy")
        with pytest.raises(ValueError, match="Unknown runtime"):
            build_executor_config(ep, "unknown-runtime", "model")
