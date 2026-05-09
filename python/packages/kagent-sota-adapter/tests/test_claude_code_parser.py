"""Tests for ClaudeCodeEventParser."""

from kagent.sota_adapter.parsers._claude_code import ClaudeCodeEventParser


def _parser():
    return ClaudeCodeEventParser()


class TestGetCommand:
    def test_basic_prompt(self):
        cmd = _parser().get_command("fix the bug")
        assert cmd == ["claude", "-p", "--output-format", "stream-json", "--verbose", "fix the bug"]

    def test_prompt_with_quotes(self):
        cmd = _parser().get_command('say "hello"')
        assert 'say "hello"' in cmd


class TestGetAgentCardDefaults:
    def test_returns_required_fields(self):
        defaults = _parser().get_agent_card_defaults()
        assert defaults["name"] == "claude-code-agent"
        assert "description" in defaults
        assert "version" in defaults
        assert len(defaults["skills"]) > 0


class TestIsFinalEvent:
    def test_result_success(self):
        assert _parser().is_final_event({"type": "result", "subtype": "success"})

    def test_result_error(self):
        assert _parser().is_final_event({"type": "result", "subtype": "error"})

    def test_not_final(self):
        assert not _parser().is_final_event({"type": "assistant"})
        assert not _parser().is_final_event({"type": "system", "subtype": "init"})


class TestParseLineSystem:
    def test_system_init_ignored(self):
        events = _parser().parse_line({"type": "system", "subtype": "init", "session_id": "abc"})
        assert events == []


class TestParseLineAssistant:
    def test_text_content(self):
        raw = {
            "type": "assistant",
            "message": {
                "content": [{"type": "text", "text": "Hello!"}],
            },
            "session_id": "s1",
        }
        events = _parser().parse_line(raw)
        assert len(events) == 1
        assert events[0].event_type == "message"
        assert events[0].text == "Hello!"

    def test_thinking_content(self):
        raw = {
            "type": "assistant",
            "message": {
                "content": [{"type": "thinking", "thinking": "Let me consider..."}],
            },
            "session_id": "s1",
        }
        events = _parser().parse_line(raw)
        assert len(events) == 1
        assert events[0].event_type == "reasoning"
        assert events[0].text == "Let me consider..."

    def test_tool_use(self):
        raw = {
            "type": "assistant",
            "message": {
                "content": [
                    {"type": "tool_use", "id": "tu_1", "name": "Bash", "input": {"command": "ls"}}
                ],
            },
            "session_id": "s1",
        }
        events = _parser().parse_line(raw)
        assert len(events) == 1
        assert events[0].event_type == "tool_call"
        assert events[0].tool_name == "Bash"
        assert events[0].tool_args == {"command": "ls"}
        assert events[0].item_id == "tu_1"

    def test_tool_result(self):
        raw = {
            "type": "assistant",
            "message": {
                "content": [
                    {"type": "tool_result", "tool_use_id": "tu_1", "content": "file.txt\n"}
                ],
            },
            "session_id": "s1",
        }
        events = _parser().parse_line(raw)
        assert len(events) == 1
        assert events[0].event_type == "tool_output"
        assert events[0].tool_output == "file.txt\n"
        assert events[0].item_id == "tu_1"

    def test_mixed_content_blocks(self):
        raw = {
            "type": "assistant",
            "message": {
                "content": [
                    {"type": "thinking", "thinking": "hmm"},
                    {"type": "text", "text": "Here's the plan"},
                    {"type": "tool_use", "id": "tu_2", "name": "Read", "input": {"file": "x.py"}},
                ],
            },
            "session_id": "s1",
        }
        events = _parser().parse_line(raw)
        assert len(events) == 3
        assert events[0].event_type == "reasoning"
        assert events[1].event_type == "message"
        assert events[2].event_type == "tool_call"

    def test_empty_content(self):
        raw = {
            "type": "assistant",
            "message": {"content": []},
            "session_id": "s1",
        }
        events = _parser().parse_line(raw)
        assert events == []


class TestParseLineResult:
    def test_success(self):
        raw = {
            "type": "result",
            "subtype": "success",
            "result": "Done! The file has been updated.",
            "duration_ms": 5000,
            "num_turns": 2,
            "total_cost_usd": 0.05,
        }
        events = _parser().parse_line(raw)
        assert len(events) == 1
        assert events[0].event_type == "message"
        assert events[0].text == "Done! The file has been updated."
        assert events[0].metadata["duration_ms"] == 5000

    def test_error(self):
        raw = {
            "type": "result",
            "subtype": "error",
            "error": "API key invalid",
        }
        events = _parser().parse_line(raw)
        assert len(events) == 1
        assert events[0].event_type == "error"
        assert events[0].text == "API key invalid"

    def test_success_empty_result(self):
        raw = {"type": "result", "subtype": "success", "result": ""}
        events = _parser().parse_line(raw)
        assert events == []


class TestFullStream:
    """Simulate a full Claude Code session and verify event stream."""

    def test_hello_session(self):
        """Reproduce the actual output from `claude -p "Say hello"`."""
        stream = [
            {
                "type": "system",
                "subtype": "init",
                "session_id": "abc-123",
                "tools": ["Bash", "Read", "Write"],
                "model": "claude-opus-4-6",
            },
            {
                "type": "assistant",
                "message": {
                    "content": [{"type": "thinking", "thinking": "The user is just saying hello."}],
                },
                "session_id": "abc-123",
            },
            {
                "type": "assistant",
                "message": {
                    "content": [{"type": "text", "text": "Hello! How can I help you today?"}],
                },
                "session_id": "abc-123",
            },
            {
                "type": "result",
                "subtype": "success",
                "result": "Hello! How can I help you today?",
                "duration_ms": 2953,
                "num_turns": 1,
                "total_cost_usd": 0.18,
            },
        ]

        parser = _parser()
        all_events = []
        for raw in stream:
            all_events.extend(parser.parse_line(raw))
            if parser.is_final_event(raw):
                break

        # system → skip, thinking → reasoning, text → message, result → message
        assert len(all_events) == 3
        assert all_events[0].event_type == "reasoning"
        assert all_events[1].event_type == "message"
        assert all_events[1].text == "Hello! How can I help you today?"
        assert all_events[2].event_type == "message"  # final result
