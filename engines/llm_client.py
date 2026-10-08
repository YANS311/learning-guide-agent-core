"""
llm_client.py
LLM 调用统一封装

支持本地 Ollama 和远程 OpenAI 兼容接口
"""

from __future__ import annotations

import asyncio
import json
import logging
import os
from typing import Any, Dict, List, Optional

from openai import AsyncOpenAI


logger = logging.getLogger("LLMClient")


class LLMClient:
    """统一的 LLM 调用客户端，支持多 Provider (Ollama / vLLM / OpenAI API / DeepSeek / 硅基流动等)"""

    def __init__(self, model: Optional[str] = None):
        self.provider = os.getenv("LLM_PROVIDER", "").lower()
        self.api_key = os.getenv("LLM_API_KEY") or os.getenv("OPENAI_API_KEY") or "ollama"
        self.base_url = os.getenv("LLM_BASE_URL") or os.getenv("OPENAI_BASE_URL") or "http://localhost:11434/v1"
        self.model = model or os.getenv("LLM_MODEL") or os.getenv("OLLAMA_MODEL") or "qwen2.5:7b"
        self.client = AsyncOpenAI(api_key=self.api_key, base_url=self.base_url, max_retries=0)
        self.is_local = "localhost" in self.base_url or "127.0.0.1" in self.base_url
        if not self.provider:
            self.provider = "ollama" if self.is_local else "openai"

        logger.info(
            "[LLMClient] Provider=%s, 模型=%s, 接口=%s, 本地=%s",
            self.provider,
            self.model,
            self.base_url,
            self.is_local,
        )

    async def chat(
        self,
        messages: List[Dict[str, str]],
        max_tokens: int = 1500,
        temperature: float = 0.3,
        json_mode: bool = False,
        timeout: float = 30.0,
    ) -> str:
        """调用 LLM 获取回复（含重试 + 超时）"""
        kwargs: Dict[str, Any] = {
            "model": self.model,
            "messages": messages,
            "max_completion_tokens": max_tokens,
            "temperature": temperature,
        }
        if json_mode and not self.is_local and self.provider != "ollama":
            kwargs["response_format"] = {"type": "json_object"}

        last_error = None
        for attempt in range(2):
            try:
                response = await asyncio.wait_for(
                    self.client.chat.completions.create(**kwargs),
                    timeout=timeout,
                )
                content = (response.choices[0].message.content or "").strip()
                return content
            except asyncio.TimeoutError:
                last_error = f"LLM 调用超时（{timeout}s）"
                logger.warning("[LLM] 超时 (attempt %d)", attempt + 1)
            except Exception as e:
                status = getattr(e, "status_code", 0)
                if status and 400 <= status < 500:
                    logger.error("[LLM] 不可重试错误，状态码: %s", status)
                    raise
                last_error = e
                logger.warning("[LLM] 失败 (attempt %d): %s", attempt + 1, type(e).__name__)
            if attempt == 0:
                await asyncio.sleep(1)
        raise Exception(last_error)

    async def chat_json(
        self,
        system_prompt: str,
        user_prompt: str,
        max_tokens: int = 500,
    ) -> Dict[str, Any]:
        """调用 LLM 并解析 JSON 返回"""
        messages = [
            {"role": "system", "content": system_prompt},
            {"role": "user", "content": user_prompt},
        ]
        raw = await self.chat(messages, max_tokens=max_tokens, json_mode=True)
        return self._parse_json(raw)

    @staticmethod
    def _parse_json(content: str) -> Dict[str, Any]:
        """从 LLM 输出中提取 JSON"""
        cleaned = content.replace("```json", "").replace("```", "").strip()
        start = cleaned.find("{")
        end = cleaned.rfind("}")
        if start != -1 and end != -1 and end >= start:
            try:
                return json.loads(cleaned[start:end + 1])
            except json.JSONDecodeError:
                pass
        return {"raw": content}
