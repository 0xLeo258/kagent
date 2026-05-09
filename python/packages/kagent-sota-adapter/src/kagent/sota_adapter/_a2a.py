"""KAgent SOTA Adapter Application.

Provides the KAgentApp class for building FastAPI applications that bridge
CLI-based AI agents to the A2A (Agent-to-Agent) protocol.
"""

from __future__ import annotations

import faulthandler
import logging
import os

import httpx
from a2a.server.apps import A2AFastAPIApplication
from a2a.server.request_handlers import DefaultRequestHandler
from a2a.server.tasks import InMemoryTaskStore
from a2a.types import AgentCard
from fastapi import FastAPI, Request
from fastapi.responses import PlainTextResponse
from kagent.core import KAgentConfig, configure_tracing
from kagent.core.a2a import (
    KAgentRequestContextBuilder,
    KAgentTaskStore,
    get_a2a_max_content_length,
)

from ._event_parser import EventParser
from ._executor import CLIAgentExecutor, CLIAgentExecutorConfig

logger = logging.getLogger(__name__)


def _health_check(request: Request) -> PlainTextResponse:
    """Health check endpoint."""
    return PlainTextResponse("OK")


def _thread_dump(request: Request) -> PlainTextResponse:
    """Thread dump endpoint for debugging."""
    import tempfile

    with tempfile.TemporaryFile(mode="w+") as tmp:
        faulthandler.dump_traceback(file=tmp, all_threads=True)
        tmp.seek(0)
        return PlainTextResponse(tmp.read())


kagent_url_override = os.getenv("KAGENT_URL")


class KAgentApp:
    """FastAPI application builder for CLI-based agents with KAgent integration.

    Wraps a CLI agent (identified by an EventParser) into a full A2A-compatible
    HTTP server, following the same pattern as kagent-openai and kagent-langgraph.
    """

    def __init__(
        self,
        parser: EventParser,
        agent_card: AgentCard,
        config: KAgentConfig,
        executor_config: CLIAgentExecutorConfig | None = None,
        tracing: bool = True,
    ):
        """Initialize the KAgent application.

        Args:
            parser: EventParser implementation for the target CLI agent.
            agent_card: A2A agent card describing capabilities.
            config: KAgent configuration (app name, namespace, backend URL).
            executor_config: Optional executor configuration (timeout, env, etc.).
            tracing: Whether to enable OpenTelemetry tracing.
        """
        self.parser = parser
        self.agent_card = AgentCard.model_validate(agent_card)
        self.config = config
        self.executor_config = executor_config or CLIAgentExecutorConfig()
        self.tracing = tracing

    def build(self) -> FastAPI:
        """Build a production FastAPI application with KAgent integration.

        Creates an application that:
        - Connects to KAgent backend for task persistence
        - Implements A2A protocol handlers
        - Spawns CLI agent subprocesses per request
        - Includes health check endpoints

        Returns:
            Configured FastAPI application.
        """
        http_client = httpx.AsyncClient(
            base_url=kagent_url_override or self.config.kagent_url,
        )

        agent_executor = CLIAgentExecutor(
            parser=self.parser,
            app_name=self.config.app_name,
            config=self.executor_config,
        )

        kagent_task_store = KAgentTaskStore(http_client)

        request_context_builder = KAgentRequestContextBuilder(task_store=kagent_task_store)
        request_handler = DefaultRequestHandler(
            agent_executor=agent_executor,
            task_store=kagent_task_store,
            request_context_builder=request_context_builder,
        )

        max_content_length = get_a2a_max_content_length()
        a2a_app = A2AFastAPIApplication(
            agent_card=self.agent_card,
            http_handler=request_handler,
            max_content_length=max_content_length,
        )

        faulthandler.enable()
        app = FastAPI()

        if self.tracing:
            try:
                configure_tracing(self.config.name, self.config.namespace, app)
            except Exception as e:
                logger.error(f"Failed to configure tracing: {e}")

        app.add_route("/health", methods=["GET"], route=_health_check)
        app.add_route("/thread_dump", methods=["GET"], route=_thread_dump)
        a2a_app.add_routes_to_app(app)

        return app

    def build_local(self) -> FastAPI:
        """Build a local FastAPI application for testing without KAgent backend.

        Uses InMemoryTaskStore instead of KAgentTaskStore, so no backend is needed.

        Returns:
            Configured FastAPI application for local use.
        """
        agent_executor = CLIAgentExecutor(
            parser=self.parser,
            app_name=self.config.app_name,
            config=self.executor_config,
        )

        task_store = InMemoryTaskStore()

        request_context_builder = KAgentRequestContextBuilder(task_store=task_store)
        request_handler = DefaultRequestHandler(
            agent_executor=agent_executor,
            task_store=task_store,
            request_context_builder=request_context_builder,
        )

        max_content_length = get_a2a_max_content_length()
        a2a_app = A2AFastAPIApplication(
            agent_card=self.agent_card,
            http_handler=request_handler,
            max_content_length=max_content_length,
        )

        faulthandler.enable()
        app = FastAPI()

        app.add_route("/health", methods=["GET"], route=_health_check)
        app.add_route("/thread_dump", methods=["GET"], route=_thread_dump)
        a2a_app.add_routes_to_app(app)

        return app
