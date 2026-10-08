import asyncio
from types import SimpleNamespace
from unittest.mock import AsyncMock

import pytest

from engines.llm_client import LLMClient


def test_model_client_with_mocked_transport(monkeypatch):
    monkeypatch.setenv("LLM_API_KEY", "test-only-placeholder")
    monkeypatch.setenv("LLM_BASE_URL", "http://localhost:11434/v1")
    monkeypatch.setenv("LLM_MODEL", "demo-model")
    client = LLMClient()
    completion = AsyncMock(return_value=SimpleNamespace(
        choices=[SimpleNamespace(message=SimpleNamespace(content="  示例回答  "))],
    ))
    client.client = SimpleNamespace(chat=SimpleNamespace(completions=SimpleNamespace(create=completion)))
    assert asyncio.run(client.chat([{"role": "user", "content": "demo"}])) == "示例回答"
    assert completion.call_args.kwargs["model"] == "demo-model"


def test_provider_client_error_is_not_retried(monkeypatch):
    monkeypatch.setenv("LLM_API_KEY", "test-only-placeholder")
    client = LLMClient()

    class RejectedRequest(Exception):
        status_code = 401

    completion = AsyncMock(side_effect=RejectedRequest())
    client.client = SimpleNamespace(chat=SimpleNamespace(completions=SimpleNamespace(create=completion)))
    with pytest.raises(RejectedRequest):
        asyncio.run(client.chat([{"role": "user", "content": "demo"}]))
    assert completion.call_count == 1
