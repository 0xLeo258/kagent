"""Tests for CodexEventParser."""

from kagent.sota_adapter.parsers._codex import CodexEventParser


def _parser():
    return CodexEventParser()


class TestGetCommand:
    def test_basic_prompt(self):
        cmd = _parser().get_command("fix the bug")
        assert cmd == ["codex", "exec", "--json", "--full-auto", "--ephemeral", "--skip-git-repo-check", "fix the bug"]

    def test_prompt_with_quotes(self):
        cmd = _parser().get_command('say "hello"')
        assert "say \"hello\"" in cmd


class TestGetAgentCardDefaults:
    def test_returns_required_fields(self):
        defaults = _parser().get_agent_card_defaults()
        assert "name" in defaults
        assert "description" in defaults
        assert "version" in defaults
        assert "skills" in defaults
        assert len(defaults["skills"]) > 0


class TestIsFinalEvent:
    def test_turn_completed(self):
        assert _parser().is_final_event({"type": "turn.completed", "usage": {}})

    def test_turn_failed(self):
        assert _parser().is_final_event({"type": "turn.failed", "error": {"message": "fail"}})

    def test_not_final(self):
        assert not _parser().is_final_event({"type": "item.completed"})
        assert not _parser().is_final_event({"type": "thread.started"})


class TestParseLineThreadAndTurn:
    def test_thread_started_ignored(self):
        events = _parser().parse_line({"type": "thread.started", "thread_id": "abc"})
        assert events == []

    def test_turn_started_ignored(self):
        events = _parser().parse_line({"type": "turn.started"})
        assert events == []

    def test_turn_completed_no_events(self):
        events = _parser().parse_line({
            "type": "turn.completed",
            "usage": {"input_tokens": 100, "output_tokens": 50},
        })
        assert events == []

    def test_turn_failed_emits_error(self):
        events = _parser().parse_line({
            "type": "turn.failed",
            "error": {"message": "context window exceeded"},
        })
        assert len(events) == 1
        assert events[0].event_type == "error"
        assert events[0].text == "context window exceeded"


class TestParseLineItemAgentMessage:
    def test_agent_message_completed(self):
        events = _parser().parse_line({
            "type": "item.completed",
            "item": {
                "id": "item_1",
                "type": "agent_message",
                "text": "Here is the fix.",
            },
        })
        assert len(events) == 1
        assert events[0].event_type == "message"
        assert events[0].text == "Here is the fix."
        assert events[0].item_id == "item_1"

    def test_agent_message_empty_text_skipped(self):
        events = _parser().parse_line({
            "type": "item.completed",
            "item": {"id": "item_2", "type": "agent_message", "text": ""},
        })
        assert events == []


class TestParseLineItemCommandExecution:
    def test_command_started(self):
        events = _parser().parse_line({
            "type": "item.started",
            "item": {
                "id": "item_3",
                "type": "command_execution",
                "command": "bash -lc ls",
                "status": "in_progress",
            },
        })
        assert len(events) == 1
        assert events[0].event_type == "tool_call"
        assert events[0].tool_name == "command_execution"
        assert events[0].tool_args == {"command": "bash -lc ls"}

    def test_command_completed(self):
        events = _parser().parse_line({
            "type": "item.completed",
            "item": {
                "id": "item_3",
                "type": "command_execution",
                "command": "bash -lc ls",
                "aggregated_output": "file1.py\nfile2.py\n",
                "exit_code": 0,
                "status": "completed",
            },
        })
        assert len(events) == 1
        assert events[0].event_type == "tool_output"
        assert events[0].tool_output == "file1.py\nfile2.py\n"
        assert events[0].metadata["exit_code"] == 0


class TestParseLineItemFileChange:
    def test_file_change_started(self):
        events = _parser().parse_line({
            "type": "item.started",
            "item": {
                "id": "item_4",
                "type": "file_change",
                "changes": [{"path": "src/main.py", "kind": "modified"}],
                "status": "in_progress",
            },
        })
        assert len(events) == 1
        assert events[0].event_type == "tool_call"
        assert events[0].tool_args == {"files": ["src/main.py"]}

    def test_file_change_completed(self):
        events = _parser().parse_line({
            "type": "item.completed",
            "item": {
                "id": "item_4",
                "type": "file_change",
                "changes": [{"path": "src/main.py", "kind": "modified"}],
                "status": "completed",
            },
        })
        assert len(events) == 1
        assert events[0].event_type == "tool_output"
        assert "modified: src/main.py" in events[0].tool_output


class TestParseLineItemMcpToolCall:
    def test_mcp_tool_call_started(self):
        events = _parser().parse_line({
            "type": "item.started",
            "item": {
                "id": "item_5",
                "type": "mcp_tool_call",
                "server": "github",
                "tool": "search_repos",
                "arguments": {"query": "kagent"},
                "status": "in_progress",
            },
        })
        assert len(events) == 1
        assert events[0].event_type == "tool_call"
        assert events[0].tool_name == "mcp:github/search_repos"
        assert events[0].tool_args == {"query": "kagent"}

    def test_mcp_tool_call_completed_with_error(self):
        events = _parser().parse_line({
            "type": "item.completed",
            "item": {
                "id": "item_5",
                "type": "mcp_tool_call",
                "server": "github",
                "tool": "search_repos",
                "error": {"message": "rate limited"},
                "status": "failed",
            },
        })
        assert len(events) == 1
        assert events[0].event_type == "tool_output"
        assert "rate limited" in events[0].tool_output


class TestParseLineItemReasoning:
    def test_reasoning_started(self):
        events = _parser().parse_line({
            "type": "item.started",
            "item": {
                "id": "item_6",
                "type": "reasoning",
                "text": "Let me think about this...",
            },
        })
        assert len(events) == 1
        assert events[0].event_type == "reasoning"
        assert events[0].text == "Let me think about this..."


class TestParseLineItemError:
    def test_error_item(self):
        events = _parser().parse_line({
            "type": "item.completed",
            "item": {
                "id": "item_7",
                "type": "error",
                "message": "Something went wrong",
            },
        })
        assert len(events) == 1
        assert events[0].event_type == "error"
        assert events[0].text == "Something went wrong"


class TestParseLineTopLevelError:
    def test_top_level_error(self):
        events = _parser().parse_line({
            "type": "error",
            "message": "failed to serialize event",
        })
        assert len(events) == 1
        assert events[0].event_type == "error"
        assert events[0].text == "failed to serialize event"


class TestFullStream:
    """Test a complete Codex JSONL stream end-to-end."""

    def test_typical_codex_session(self):
        parser = _parser()
        stream = [
            {"type": "thread.started", "thread_id": "abc-123"},
            {"type": "turn.started"},
            {"type": "item.started", "item": {"id": "i1", "type": "command_execution", "command": "ls", "status": "in_progress"}},
            {"type": "item.completed", "item": {"id": "i1", "type": "command_execution", "aggregated_output": "README.md", "exit_code": 0, "status": "completed"}},
            {"type": "item.completed", "item": {"id": "i2", "type": "agent_message", "text": "The repo contains README.md."}},
            {"type": "turn.completed", "usage": {"input_tokens": 1000, "output_tokens": 50, "cached_input_tokens": 800}},
        ]

        all_events = []
        for line in stream:
            all_events.extend(parser.parse_line(line))

        # Should have: tool_call, tool_output, message = 3 events
        assert len(all_events) == 3
        assert all_events[0].event_type == "tool_call"
        assert all_events[1].event_type == "tool_output"
        assert all_events[2].event_type == "message"
        assert all_events[2].text == "The repo contains README.md."

        # turn.completed should be final
        assert parser.is_final_event(stream[-1])
