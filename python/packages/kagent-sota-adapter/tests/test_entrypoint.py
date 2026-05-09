"""Tests for the generic __main__.py entrypoint."""

import os
from unittest.mock import patch

import pytest

from kagent.sota_adapter.__main__ import _detect_runtime


class TestDetectRuntime:
    def test_explicit_codex(self):
        with patch.dict(os.environ, {"CLI_RUNTIME": "codex"}):
            assert _detect_runtime() == "codex"

    def test_explicit_claude_code(self):
        with patch.dict(os.environ, {"CLI_RUNTIME": "claude-code"}):
            assert _detect_runtime() == "claude-code"

    def test_explicit_case_insensitive(self):
        with patch.dict(os.environ, {"CLI_RUNTIME": "CODEX"}):
            assert _detect_runtime() == "codex"

    def test_explicit_unknown_exits(self):
        with patch.dict(os.environ, {"CLI_RUNTIME": "unknown"}):
            with pytest.raises(SystemExit):
                _detect_runtime()

    def test_auto_detect_codex_binary(self):
        with (
            patch.dict(os.environ, {}, clear=False),
            patch("kagent.sota_adapter.__main__.shutil") as mock_shutil,
        ):
            os.environ.pop("CLI_RUNTIME", None)
            mock_shutil.which.side_effect = lambda b: "/usr/local/bin/codex" if b == "codex" else None
            assert _detect_runtime() == "codex"

    def test_auto_detect_claude_binary(self):
        with (
            patch.dict(os.environ, {}, clear=False),
            patch("kagent.sota_adapter.__main__.shutil") as mock_shutil,
        ):
            os.environ.pop("CLI_RUNTIME", None)
            mock_shutil.which.side_effect = lambda b: "/usr/local/bin/claude" if b == "claude" else None
            assert _detect_runtime() == "claude-code"

    def test_no_runtime_no_binary_exits(self):
        with (
            patch.dict(os.environ, {}, clear=False),
            patch("kagent.sota_adapter.__main__.shutil") as mock_shutil,
        ):
            os.environ.pop("CLI_RUNTIME", None)
            mock_shutil.which.return_value = None
            with pytest.raises(SystemExit):
                _detect_runtime()
