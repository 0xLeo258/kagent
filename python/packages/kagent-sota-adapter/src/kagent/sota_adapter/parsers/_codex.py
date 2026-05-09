"""OpenAI Codex CLI event parser.

Parses JSONL output from `codex exec --json` into normalized CLIEvents.

Codex JSONL event types:
- thread.started  — session begins (thread_id)
- turn.started    — agent turn begins
- item.started    — work item begins (agent_message, command_execution, file_change, etc.)
- item.updated    — work item state change
- item.completed  — work item finishes
- turn.completed  — turn finishes (usage stats)
- turn.failed     — turn error
- error           — critical error
"""

from __future__ import annotations

import logging

try:
    from typing import override
except ImportError:
    from typing_extensions import override

from .._event_parser import CLIEvent, EventParser

logger = logging.getLogger(__name__)


class CodexEventParser(EventParser):
    """Parses OpenAI Codex CLI JSONL output into CLIEvents."""

    @override
    def get_command(self, prompt: str) -> list[str]:
        return [
            "codex", "exec",
            "--json",
            "--full-auto",
            "--ephemeral",
            "--skip-git-repo-check",
            prompt,
        ]

    @override
    def get_agent_card_defaults(self) -> dict:
        return {
            "name": "codex-agent",
            "description": "OpenAI Codex coding agent",
            "version": "1.0.0",
            "skills": [
                {
                    "id": "coding",
                    "name": "Code Generation & Editing",
                    "description": "Generate, edit, and refactor code using OpenAI Codex",
                    "tags": ["code", "codex", "openai"],
                }
            ],
        }

    @override
    def is_final_event(self, raw: dict) -> bool:
        return raw.get("type") in ("turn.completed", "turn.failed")

    @override
    def parse_line(self, raw: dict) -> list[CLIEvent]:
        event_type = raw.get("type", "")

        if event_type == "item.started":
            return self._parse_item_started(raw)
        elif event_type == "item.completed":
            return self._parse_item_completed(raw)
        elif event_type == "item.updated":
            return self._parse_item_updated(raw)
        elif event_type == "turn.failed":
            return self._parse_turn_failed(raw)
        elif event_type == "turn.completed":
            return self._parse_turn_completed(raw)
        elif event_type == "error":
            return self._parse_error(raw)
        elif event_type in ("thread.started", "turn.started"):
            return []  # Metadata-only, skip
        else:
            logger.debug(f"Unhandled Codex event type: {event_type}")
            return []

    def _parse_item_started(self, raw: dict) -> list[CLIEvent]:
        """Parse item.started — emit tool_call for commands/file changes/mcp calls."""
        item = raw.get("item", {})
        item_type = item.get("type", "")
        item_id = item.get("id", "")

        if item_type == "command_execution":
            command = item.get("command", "")
            return [CLIEvent(
                event_type="tool_call",
                tool_name="command_execution",
                tool_args={"command": command},
                item_id=item_id,
                raw=raw,
            )]

        elif item_type == "file_change":
            changes = item.get("changes", [])
            paths = [c.get("path", "") for c in changes]
            return [CLIEvent(
                event_type="tool_call",
                tool_name="file_change",
                tool_args={"files": paths},
                item_id=item_id,
                raw=raw,
            )]

        elif item_type == "mcp_tool_call":
            server = item.get("server", "")
            tool = item.get("tool", "")
            arguments = item.get("arguments", {})
            return [CLIEvent(
                event_type="tool_call",
                tool_name=f"mcp:{server}/{tool}",
                tool_args=arguments,
                item_id=item_id,
                raw=raw,
            )]

        elif item_type == "reasoning":
            text = item.get("text", "")
            if text:
                return [CLIEvent(
                    event_type="reasoning",
                    text=text,
                    item_id=item_id,
                    raw=raw,
                )]

        return []

    def _parse_item_completed(self, raw: dict) -> list[CLIEvent]:
        """Parse item.completed — emit messages, tool outputs, and final answers."""
        item = raw.get("item", {})
        item_type = item.get("type", "")
        item_id = item.get("id", "")

        if item_type == "agent_message":
            text = item.get("text", "")
            if text:
                return [CLIEvent(
                    event_type="message",
                    text=text,
                    item_id=item_id,
                    raw=raw,
                )]

        elif item_type == "command_execution":
            output = item.get("aggregated_output", "")
            exit_code = item.get("exit_code")
            status = item.get("status", "completed")
            return [CLIEvent(
                event_type="tool_output",
                tool_name="command_execution",
                tool_output=output,
                item_id=item_id,
                metadata={"exit_code": exit_code, "status": status},
                raw=raw,
            )]

        elif item_type == "file_change":
            status = item.get("status", "completed")
            changes = item.get("changes", [])
            summary = ", ".join(f"{c.get('kind', '?')}: {c.get('path', '?')}" for c in changes)
            return [CLIEvent(
                event_type="tool_output",
                tool_name="file_change",
                tool_output=summary or f"File change {status}",
                item_id=item_id,
                metadata={"status": status},
                raw=raw,
            )]

        elif item_type == "mcp_tool_call":
            result = item.get("result", {})
            error = item.get("error")
            if error:
                return [CLIEvent(
                    event_type="tool_output",
                    tool_name=f"mcp:{item.get('server', '')}/{item.get('tool', '')}",
                    tool_output=f"Error: {error.get('message', '')}",
                    item_id=item_id,
                    raw=raw,
                )]
            result_text = str(result.get("structured_content") or result.get("content", ""))
            return [CLIEvent(
                event_type="tool_output",
                tool_name=f"mcp:{item.get('server', '')}/{item.get('tool', '')}",
                tool_output=result_text,
                item_id=item_id,
                raw=raw,
            )]

        elif item_type == "reasoning":
            text = item.get("text", "")
            if text:
                return [CLIEvent(
                    event_type="reasoning",
                    text=text,
                    item_id=item_id,
                    raw=raw,
                )]

        elif item_type == "error":
            return [CLIEvent(
                event_type="error",
                text=item.get("message", "Unknown error"),
                item_id=item_id,
                raw=raw,
            )]

        return []

    def _parse_item_updated(self, raw: dict) -> list[CLIEvent]:
        """Parse item.updated — same structure as item.started, re-emit if meaningful."""
        # For now, treat updates the same as started events for streaming progress
        return []

    def _parse_turn_failed(self, raw: dict) -> list[CLIEvent]:
        """Parse turn.failed — emit error event."""
        error = raw.get("error", {})
        message = error.get("message", "Turn failed")
        return [CLIEvent(
            event_type="error",
            text=message,
            raw=raw,
        )]

    def _parse_turn_completed(self, raw: dict) -> list[CLIEvent]:
        """Parse turn.completed — no CLIEvent needed, but extract usage metadata."""
        # Usage data is informational; the executor handles completion
        usage = raw.get("usage", {})
        if usage:
            logger.info(
                f"Codex usage: input={usage.get('input_tokens', 0)}, "
                f"cached={usage.get('cached_input_tokens', 0)}, "
                f"output={usage.get('output_tokens', 0)}"
            )
        return []

    def _parse_error(self, raw: dict) -> list[CLIEvent]:
        """Parse top-level error event."""
        message = raw.get("message", "Unknown error")
        return [CLIEvent(
            event_type="error",
            text=message,
            raw=raw,
        )]
