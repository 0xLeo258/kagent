"""CLI Agent Executor for A2A Protocol.

Spawns a CLI agent as a subprocess, streams its JSONL output, and converts
events to A2A protocol events via a pluggable EventParser.
"""

from __future__ import annotations

import asyncio
import json
import logging
import uuid
from datetime import datetime

try:
    from datetime import UTC
except ImportError:
    from datetime import timezone

    UTC = timezone.utc

try:
    from typing import override
except ImportError:
    from typing_extensions import override

from a2a.server.agent_execution import AgentExecutor
from a2a.server.agent_execution.context import RequestContext
from a2a.server.events.event_queue import EventQueue
from a2a.types import (
    Artifact,
    DataPart,
    Message,
    Part,
    Role,
    TaskArtifactUpdateEvent,
    TaskState,
    TaskStatus,
    TaskStatusUpdateEvent,
    TextPart,
)
from kagent.core.a2a import (
    A2A_DATA_PART_METADATA_TYPE_FUNCTION_CALL,
    A2A_DATA_PART_METADATA_TYPE_FUNCTION_RESPONSE,
    A2A_DATA_PART_METADATA_TYPE_KEY,
    TaskResultAggregator,
    get_kagent_metadata_key,
)
from collections.abc import Callable

from pydantic import BaseModel, ConfigDict

from ._event_parser import CLIEvent, EventParser

logger = logging.getLogger(__name__)


class CLIAgentExecutorConfig(BaseModel):
    """Configuration for CLIAgentExecutor."""

    model_config = ConfigDict(arbitrary_types_allowed=True)

    execution_timeout: float = 600.0
    """Maximum seconds to wait for CLI agent to complete."""

    working_directory: str | None = None
    """Working directory for the subprocess. None uses the current directory."""

    env_vars: dict[str, str] = {}
    """Extra environment variables passed to the subprocess."""

    extra_args: list[str] = []
    """Additional CLI arguments appended to the command."""

    pre_execute: Callable[[], dict[str, str]] | None = None
    """Optional callback invoked before each subprocess spawn.
    Returns env var overrides (e.g. a refreshed OAuth token)."""


class CLIAgentExecutor(AgentExecutor):
    """Executes CLI agents via subprocess and bridges JSONL output to A2A events."""

    def __init__(
        self,
        *,
        parser: EventParser,
        app_name: str,
        config: CLIAgentExecutorConfig | None = None,
    ):
        super().__init__()
        self._parser = parser
        self.app_name = app_name
        self._config = config or CLIAgentExecutorConfig()
        self._proc: asyncio.subprocess.Process | None = None

    async def _run_cli_agent(
        self,
        prompt: str,
        context: RequestContext,
        event_queue: EventQueue,
    ) -> None:
        """Spawn the CLI agent and stream its JSONL output as A2A events."""
        import os

        cmd = self._parser.get_command(prompt)
        if self._config.extra_args:
            # Insert extra_args before the prompt (last element) so CLI flags
            # like -c and -m are parsed before the positional prompt arg.
            prompt_arg = cmd.pop()
            cmd.extend(self._config.extra_args)
            cmd.append(prompt_arg)

        env = {**os.environ, **self._config.env_vars}

        # Call pre_execute hook for dynamic env overrides (e.g. OAuth token refresh)
        if self._config.pre_execute:
            env.update(self._config.pre_execute())

        logger.info(f"Spawning CLI agent: {' '.join(cmd)}")

        self._proc = await asyncio.create_subprocess_exec(
            *cmd,
            stdout=asyncio.subprocess.PIPE,
            stderr=asyncio.subprocess.PIPE,
            cwd=self._config.working_directory,
            env=env,
        )

        task_result_aggregator = TaskResultAggregator()
        last_message_text: str | None = None

        try:
            assert self._proc.stdout is not None
            while True:
                line = await self._proc.stdout.readline()
                if not line:
                    break

                line_str = line.decode("utf-8", errors="replace").strip()
                if not line_str:
                    continue

                try:
                    raw = json.loads(line_str)
                except json.JSONDecodeError:
                    logger.debug(f"Non-JSON line from CLI agent: {line_str}")
                    continue

                # Parse through the EventParser
                cli_events = self._parser.parse_line(raw)

                for cli_event in cli_events:
                    a2a_events = _convert_cli_event_to_a2a(
                        cli_event,
                        context.task_id,
                        context.context_id,
                        self.app_name,
                    )
                    for a2a_event in a2a_events:
                        task_result_aggregator.process_event(a2a_event)
                        await event_queue.enqueue_event(a2a_event)

                    # Track the last message text for the final artifact
                    if cli_event.event_type == "message" and cli_event.text:
                        last_message_text = cli_event.text

                # Check if the CLI agent signaled completion
                if self._parser.is_final_event(raw):
                    break

            # Wait for process to finish
            await self._proc.wait()

            # Read stderr for diagnostics
            if self._proc.stderr:
                stderr_data = await self._proc.stderr.read()
                if stderr_data:
                    stderr_text = stderr_data.decode("utf-8", errors="replace").strip()
                    if stderr_text:
                        logger.warning(f"CLI agent stderr: {stderr_text[:500]}")

            # Check exit code
            if self._proc.returncode and self._proc.returncode != 0:
                raise RuntimeError(f"CLI agent exited with code {self._proc.returncode}")

            # Emit final artifact + completed status
            if last_message_text:
                await event_queue.enqueue_event(
                    TaskArtifactUpdateEvent(
                        task_id=context.task_id,
                        last_chunk=True,
                        context_id=context.context_id,
                        artifact=Artifact(
                            artifact_id=str(uuid.uuid4()),
                            parts=[Part(TextPart(text=last_message_text))],
                        ),
                    )
                )

            await event_queue.enqueue_event(
                TaskStatusUpdateEvent(
                    task_id=context.task_id,
                    status=TaskStatus(
                        state=TaskState.completed,
                        timestamp=datetime.now(UTC).isoformat(),
                    ),
                    context_id=context.context_id,
                    final=True,
                )
            )

        except Exception:
            # Kill the process if still running
            if self._proc and self._proc.returncode is None:
                self._proc.terminate()
            raise
        finally:
            self._proc = None

    @override
    async def execute(
        self,
        context: RequestContext,
        event_queue: EventQueue,
    ) -> None:
        """Execute the CLI agent and publish updates to the event queue."""
        if not context.message:
            raise ValueError("A2A request must have a message")

        # Send submitted event for new tasks
        if not context.current_task:
            await event_queue.enqueue_event(
                TaskStatusUpdateEvent(
                    task_id=context.task_id,
                    status=TaskStatus(
                        state=TaskState.submitted,
                        message=context.message,
                        timestamp=datetime.now(UTC).isoformat(),
                    ),
                    context_id=context.context_id,
                    final=False,
                )
            )

        session_id = getattr(context, "session_id", None) or context.context_id
        user_id = getattr(context, "user_id", "admin@kagent.dev")

        # Send working status
        await event_queue.enqueue_event(
            TaskStatusUpdateEvent(
                task_id=context.task_id,
                status=TaskStatus(
                    state=TaskState.working,
                    timestamp=datetime.now(UTC).isoformat(),
                ),
                context_id=context.context_id,
                final=False,
                metadata={
                    get_kagent_metadata_key("app_name"): self.app_name,
                    get_kagent_metadata_key("session_id"): session_id,
                    get_kagent_metadata_key("user_id"): user_id,
                },
            )
        )

        try:
            user_input = context.get_user_input()

            await asyncio.wait_for(
                self._run_cli_agent(user_input, context, event_queue),
                timeout=self._config.execution_timeout,
            )

        except TimeoutError:
            logger.error(f"CLI agent timed out after {self._config.execution_timeout}s")
            await event_queue.enqueue_event(
                TaskStatusUpdateEvent(
                    task_id=context.task_id,
                    status=TaskStatus(
                        state=TaskState.failed,
                        timestamp=datetime.now(UTC).isoformat(),
                        message=Message(
                            message_id=str(uuid.uuid4()),
                            role=Role.agent,
                            parts=[Part(TextPart(text="CLI agent execution timed out"))],
                        ),
                    ),
                    context_id=context.context_id,
                    final=True,
                )
            )
        except Exception as e:
            logger.error(f"CLI agent execution failed: {e}", exc_info=True)
            error_message = str(e)
            await event_queue.enqueue_event(
                TaskStatusUpdateEvent(
                    task_id=context.task_id,
                    status=TaskStatus(
                        state=TaskState.failed,
                        timestamp=datetime.now(UTC).isoformat(),
                        message=Message(
                            message_id=str(uuid.uuid4()),
                            role=Role.agent,
                            parts=[Part(TextPart(text=f"Execution failed: {error_message}"))],
                            metadata={
                                get_kagent_metadata_key("error_type"): type(e).__name__,
                                get_kagent_metadata_key("error_detail"): error_message,
                            },
                        ),
                    ),
                    context_id=context.context_id,
                    final=True,
                    metadata={
                        get_kagent_metadata_key("error_type"): type(e).__name__,
                        get_kagent_metadata_key("error_detail"): error_message,
                    },
                )
            )

    @override
    async def cancel(self, context: RequestContext, event_queue: EventQueue) -> None:
        """Cancel execution by terminating the subprocess."""
        if self._proc and self._proc.returncode is None:
            logger.info("Cancelling CLI agent subprocess")
            self._proc.terminate()
            try:
                await asyncio.wait_for(self._proc.wait(), timeout=5.0)
            except TimeoutError:
                self._proc.kill()

            await event_queue.enqueue_event(
                TaskStatusUpdateEvent(
                    task_id=context.task_id,
                    status=TaskStatus(
                        state=TaskState.failed,
                        timestamp=datetime.now(UTC).isoformat(),
                        message=Message(
                            message_id=str(uuid.uuid4()),
                            role=Role.agent,
                            parts=[Part(TextPart(text="Execution cancelled"))],
                        ),
                    ),
                    context_id=context.context_id,
                    final=True,
                )
            )
        else:
            raise NotImplementedError("No active subprocess to cancel")


def _convert_cli_event_to_a2a(
    cli_event: CLIEvent,
    task_id: str,
    context_id: str,
    app_name: str,
) -> list[TaskStatusUpdateEvent]:
    """Convert a normalized CLIEvent to A2A TaskStatusUpdateEvents."""
    events: list[TaskStatusUpdateEvent] = []

    try:
        if cli_event.event_type == "message":
            events.append(_make_text_event(cli_event.text or "", task_id, context_id, app_name, "message"))

        elif cli_event.event_type == "reasoning":
            events.append(_make_text_event(cli_event.text or "", task_id, context_id, app_name, "reasoning"))

        elif cli_event.event_type == "tool_call":
            events.append(_make_tool_call_event(cli_event, task_id, context_id, app_name))

        elif cli_event.event_type == "tool_output":
            events.append(_make_tool_output_event(cli_event, task_id, context_id, app_name))

        elif cli_event.event_type == "error":
            events.append(_make_text_event(cli_event.text or "Unknown error", task_id, context_id, app_name, "error"))

    except Exception as e:
        logger.error(f"Error converting CLI event to A2A: {e}", exc_info=True)

    return events


def _make_text_event(
    text: str,
    task_id: str,
    context_id: str,
    app_name: str,
    event_type: str,
) -> TaskStatusUpdateEvent:
    """Create a TaskStatusUpdateEvent with a text message."""
    return TaskStatusUpdateEvent(
        task_id=task_id,
        context_id=context_id,
        status=TaskStatus(
            state=TaskState.working,
            message=Message(
                message_id=str(uuid.uuid4()),
                role=Role.agent,
                parts=[Part(TextPart(text=text))],
                metadata={
                    get_kagent_metadata_key("app_name"): app_name,
                    get_kagent_metadata_key("event_type"): event_type,
                },
            ),
            timestamp=datetime.now(UTC).isoformat(),
        ),
        metadata={get_kagent_metadata_key("app_name"): app_name},
        final=False,
    )


def _make_tool_call_event(
    cli_event: CLIEvent,
    task_id: str,
    context_id: str,
    app_name: str,
) -> TaskStatusUpdateEvent:
    """Create a TaskStatusUpdateEvent for a tool/command invocation."""
    call_id = cli_event.item_id or str(uuid.uuid4())
    function_data = {
        "id": call_id,
        "name": cli_event.tool_name or "cli_command",
        "args": cli_event.tool_args or {},
    }

    data_part = DataPart(
        data=function_data,
        metadata={
            get_kagent_metadata_key(A2A_DATA_PART_METADATA_TYPE_KEY): A2A_DATA_PART_METADATA_TYPE_FUNCTION_CALL,
        },
    )

    return TaskStatusUpdateEvent(
        task_id=task_id,
        context_id=context_id,
        status=TaskStatus(
            state=TaskState.working,
            message=Message(
                message_id=str(uuid.uuid4()),
                role=Role.agent,
                parts=[Part(data_part)],
                metadata={
                    get_kagent_metadata_key("app_name"): app_name,
                    get_kagent_metadata_key("event_type"): "tool_call",
                },
            ),
            timestamp=datetime.now(UTC).isoformat(),
        ),
        metadata={get_kagent_metadata_key("app_name"): app_name},
        final=False,
    )


def _make_tool_output_event(
    cli_event: CLIEvent,
    task_id: str,
    context_id: str,
    app_name: str,
) -> TaskStatusUpdateEvent:
    """Create a TaskStatusUpdateEvent for a tool/command result."""
    call_id = cli_event.item_id or str(uuid.uuid4())
    function_data = {
        "id": call_id,
        "name": cli_event.tool_name or "cli_command",
        "response": {"result": cli_event.tool_output or ""},
    }

    data_part = DataPart(
        data=function_data,
        metadata={
            get_kagent_metadata_key(A2A_DATA_PART_METADATA_TYPE_KEY): A2A_DATA_PART_METADATA_TYPE_FUNCTION_RESPONSE,
        },
    )

    return TaskStatusUpdateEvent(
        task_id=task_id,
        context_id=context_id,
        status=TaskStatus(
            state=TaskState.working,
            message=Message(
                message_id=str(uuid.uuid4()),
                role=Role.agent,
                parts=[Part(data_part)],
                metadata={
                    get_kagent_metadata_key("app_name"): app_name,
                    get_kagent_metadata_key("event_type"): "tool_output",
                },
            ),
            timestamp=datetime.now(UTC).isoformat(),
        ),
        metadata={get_kagent_metadata_key("app_name"): app_name},
        final=False,
    )
