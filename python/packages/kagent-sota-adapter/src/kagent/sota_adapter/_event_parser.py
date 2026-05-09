"""Event parser interface and base types for CLI-to-A2A adapters.

Defines the abstract EventParser interface and CLIEvent dataclass that normalize
CLI-specific JSONL output into a common format for A2A event conversion.
"""

from __future__ import annotations

from abc import ABC, abstractmethod
from dataclasses import dataclass, field


@dataclass
class CLIEvent:
    """A normalized event parsed from a CLI agent's JSONL output.

    All CLI-specific formats (Codex, Claude Code, etc.) are converted to this
    common structure before being mapped to A2A protocol events.
    """

    event_type: str
    """Event category: "message", "tool_call", "tool_output", "reasoning", "error", "usage"."""

    text: str | None = None
    """Text content for message, reasoning, or error events."""

    tool_name: str | None = None
    """Tool/command name for tool_call events."""

    tool_args: dict | None = None
    """Tool arguments for tool_call events."""

    tool_output: str | None = None
    """Result text for tool_output events."""

    item_id: str | None = None
    """Item ID from the CLI agent, used to correlate started/completed pairs."""

    metadata: dict = field(default_factory=dict)
    """Extra metadata (e.g., token usage, exit codes, file paths)."""

    raw: dict = field(default_factory=dict)
    """The original JSONL line, preserved for debugging."""


class EventParser(ABC):
    """Abstract interface for parsing CLI agent JSONL output.

    Each CLI agent (Codex, Claude Code, etc.) implements this interface to
    translate its specific JSONL event format into normalized CLIEvents.
    """

    @abstractmethod
    def parse_line(self, raw: dict) -> list[CLIEvent]:
        """Parse a single JSONL line into zero or more CLIEvents.

        Args:
            raw: A parsed JSON object from one line of the CLI agent's stdout.

        Returns:
            List of normalized CLIEvents. Empty list if the line should be skipped.
        """

    @abstractmethod
    def get_command(self, prompt: str) -> list[str]:
        """Build the subprocess command to invoke this CLI agent.

        Args:
            prompt: The user's input prompt.

        Returns:
            Command as a list of strings (passed to asyncio.create_subprocess_exec).
        """

    @abstractmethod
    def get_agent_card_defaults(self) -> dict:
        """Return default AgentCard fields for this CLI agent.

        Returns:
            Dict with keys like "name", "description", "version", "skills".
        """

    def is_final_event(self, raw: dict) -> bool:
        """Check if this JSONL line signals that the CLI agent has finished.

        Override this to detect agent completion without waiting for process exit.
        Default returns False (rely on process termination).

        Args:
            raw: A parsed JSON object from the CLI agent's stdout.

        Returns:
            True if the agent is done processing.
        """
        return False
