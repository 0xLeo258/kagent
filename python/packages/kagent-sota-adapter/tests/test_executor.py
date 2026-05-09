"""Tests for CLIAgentExecutor event conversion."""


from kagent.sota_adapter._event_parser import CLIEvent
from kagent.sota_adapter._executor import (
    _convert_cli_event_to_a2a,
    _make_text_event,
    _make_tool_call_event,
    _make_tool_output_event,
)

TASK_ID = "test-task-1"
CONTEXT_ID = "test-ctx-1"
APP_NAME = "test-app"


class TestMakeTextEvent:
    def test_message_event(self):
        event = _make_text_event("hello world", TASK_ID, CONTEXT_ID, APP_NAME, "message")
        assert event.task_id == TASK_ID
        assert event.context_id == CONTEXT_ID
        assert event.final is False
        assert event.status.state.value == "working"
        parts = event.status.message.parts
        assert len(parts) == 1
        assert parts[0].root.text == "hello world"

    def test_reasoning_event(self):
        event = _make_text_event("thinking...", TASK_ID, CONTEXT_ID, APP_NAME, "reasoning")
        assert event.status.message.metadata["kagent_event_type"] == "reasoning"


class TestMakeToolCallEvent:
    def test_tool_call(self):
        cli_event = CLIEvent(
            event_type="tool_call",
            tool_name="command_execution",
            tool_args={"command": "ls -la"},
            item_id="item-1",
        )
        event = _make_tool_call_event(cli_event, TASK_ID, CONTEXT_ID, APP_NAME)
        assert event.final is False
        data = event.status.message.parts[0].root.data
        assert data["name"] == "command_execution"
        assert data["args"] == {"command": "ls -la"}
        assert data["id"] == "item-1"


class TestMakeToolOutputEvent:
    def test_tool_output(self):
        cli_event = CLIEvent(
            event_type="tool_output",
            tool_name="command_execution",
            tool_output="file1.py\nfile2.py",
            item_id="item-1",
        )
        event = _make_tool_output_event(cli_event, TASK_ID, CONTEXT_ID, APP_NAME)
        data = event.status.message.parts[0].root.data
        assert data["response"]["result"] == "file1.py\nfile2.py"


class TestConvertCliEventToA2A:
    def test_message_type(self):
        cli_event = CLIEvent(event_type="message", text="done")
        events = _convert_cli_event_to_a2a(cli_event, TASK_ID, CONTEXT_ID, APP_NAME)
        assert len(events) == 1
        assert events[0].status.message.parts[0].root.text == "done"

    def test_tool_call_type(self):
        cli_event = CLIEvent(
            event_type="tool_call",
            tool_name="file_change",
            tool_args={"files": ["a.py"]},
            item_id="id1",
        )
        events = _convert_cli_event_to_a2a(cli_event, TASK_ID, CONTEXT_ID, APP_NAME)
        assert len(events) == 1

    def test_tool_output_type(self):
        cli_event = CLIEvent(
            event_type="tool_output",
            tool_name="file_change",
            tool_output="modified: a.py",
            item_id="id1",
        )
        events = _convert_cli_event_to_a2a(cli_event, TASK_ID, CONTEXT_ID, APP_NAME)
        assert len(events) == 1

    def test_error_type(self):
        cli_event = CLIEvent(event_type="error", text="something broke")
        events = _convert_cli_event_to_a2a(cli_event, TASK_ID, CONTEXT_ID, APP_NAME)
        assert len(events) == 1
        assert events[0].status.message.parts[0].root.text == "something broke"

    def test_usage_type_returns_empty(self):
        cli_event = CLIEvent(event_type="usage", metadata={"tokens": 100})
        events = _convert_cli_event_to_a2a(cli_event, TASK_ID, CONTEXT_ID, APP_NAME)
        assert events == []

    def test_unknown_type_returns_empty(self):
        cli_event = CLIEvent(event_type="unknown_type", text="x")
        events = _convert_cli_event_to_a2a(cli_event, TASK_ID, CONTEXT_ID, APP_NAME)
        assert events == []
