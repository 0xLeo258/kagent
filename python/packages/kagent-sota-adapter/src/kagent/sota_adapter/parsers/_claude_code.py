"""Claude Code CLI event parser.

Parses stream-json output from `claude -p --output-format stream-json --verbose`
into normalized CLIEvents.

Claude Code stream-json event types:
- system (subtype=init)    — session initialization
- assistant                — model response (thinking, text, tool_use)
- result (subtype=success) — final answer
- result (subtype=error)   — execution failed
"""

from __future__ import annotations

import logging

try:
    from typing import override
except ImportError:
    from typing_extensions import override

from .._event_parser import CLIEvent, EventParser

logger = logging.getLogger(__name__)


class ClaudeCodeEventParser(EventParser):
    """Parses Claude Code CLI stream-json output into CLIEvents."""

    @override
    def get_command(self, prompt: str) -> list[str]:
        return [
            "claude", "-p",
            "--output-format", "stream-json",
            "--verbose",
            prompt,
        ]

    @override
    def get_agent_card_defaults(self) -> dict:
        return {
            "name": "claude-code-agent",
            "description": "Anthropic Claude Code agent",
            "version": "1.0.0",
            "skills": [
                {
                    "id": "coding",
                    "name": "Code Generation & Editing",
                    "description": "Generate, edit, and refactor code using Claude Code",
                    "tags": ["code", "claude", "anthropic"],
                }
            ],
        }

    @override
    def is_final_event(self, raw: dict) -> bool:
        return raw.get("type") == "result"

    @override
    def parse_line(self, raw: dict) -> list[CLIEvent]:
        event_type = raw.get("type", "")

        if event_type == "system":
            return []

        if event_type == "assistant":
            return self._parse_assistant(raw)

        if event_type == "result":
            return self._parse_result(raw)

        logger.debug("Unhandled Claude Code event type: %s", event_type)
        return []

    def _parse_assistant(self, raw: dict) -> list[CLIEvent]:
        """Parse assistant message — extract text, thinking, and tool_use content blocks."""
        message = raw.get("message", {})
        content = message.get("content", [])
        if not isinstance(content, list):
            return []

        events: list[CLIEvent] = []
        session_id = raw.get("session_id", "")

        for block in content:
            if not isinstance(block, dict):
                continue

            block_type = block.get("type", "")

            if block_type == "thinking":
                text = block.get("thinking", "")
                if text:
                    events.append(CLIEvent(
                        event_type="reasoning",
                        text=text,
                        item_id=session_id,
                        raw=raw,
                    ))

            elif block_type == "text":
                text = block.get("text", "")
                if text:
                    events.append(CLIEvent(
                        event_type="message",
                        text=text,
                        item_id=session_id,
                        raw=raw,
                    ))

            elif block_type == "tool_use":
                events.append(CLIEvent(
                    event_type="tool_call",
                    tool_name=block.get("name", "unknown"),
                    tool_args=block.get("input", {}),
                    item_id=block.get("id", ""),
                    raw=raw,
                ))

            elif block_type == "tool_result":
                content_val = block.get("content", "")
                output = content_val if isinstance(content_val, str) else str(content_val)
                events.append(CLIEvent(
                    event_type="tool_output",
                    tool_name="",
                    tool_output=output,
                    item_id=block.get("tool_use_id", ""),
                    raw=raw,
                ))

        return events

    def _parse_result(self, raw: dict) -> list[CLIEvent]:
        """Parse result event — final answer or error."""
        subtype = raw.get("subtype", "")

        if subtype == "success":
            result_text = raw.get("result", "")
            if result_text:
                return [CLIEvent(
                    event_type="message",
                    text=result_text,
                    metadata={
                        "duration_ms": raw.get("duration_ms"),
                        "num_turns": raw.get("num_turns"),
                        "total_cost_usd": raw.get("total_cost_usd"),
                    },
                    raw=raw,
                )]

        elif subtype == "error":
            error_text = raw.get("error", "Unknown error")
            return [CLIEvent(
                event_type="error",
                text=error_text,
                raw=raw,
            )]

        return []
