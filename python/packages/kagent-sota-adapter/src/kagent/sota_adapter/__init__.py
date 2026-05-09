"""KAgent SOTA Adapter Package.

Generic CLI-to-A2A adapter for wrapping CLI-based AI agents
(e.g., OpenAI Codex, Claude Code) as BYO agents in kagent.
"""

from ._a2a import KAgentApp
from ._discovery import DiscoveredEndpoint, build_executor_config, discover_endpoint
from ._event_parser import CLIEvent, EventParser
from ._executor import CLIAgentExecutor, CLIAgentExecutorConfig

__all__ = [
    "KAgentApp",
    "CLIAgentExecutor",
    "CLIAgentExecutorConfig",
    "EventParser",
    "CLIEvent",
    "DiscoveredEndpoint",
    "discover_endpoint",
    "build_executor_config",
]
__version__ = "0.1.0"
