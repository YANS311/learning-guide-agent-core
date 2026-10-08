"""
base_agent.py
所有 Agent 的抽象基类

每个 Agent 实现：
1. should_handle() — 判断是否应该处理该请求
2. get_system_prompt() — 定义角色和规则
3. run() — 执行对话（可覆盖）
"""

from __future__ import annotations

import asyncio
import logging
import time
from abc import ABC, abstractmethod
from dataclasses import dataclass, field
from typing import Any, Callable, Coroutine, Dict, List, Optional

logger = logging.getLogger("BaseAgent")


@dataclass
class AgentStep:
    """Agent 推理步骤"""
    step_number: int
    thought: str
    action: Optional[str] = None
    action_input: Optional[str] = None
    observation: Optional[str] = None
    is_final: bool = False
    final_answer: Optional[str] = None


@dataclass
class AgentResult:
    """Agent 执行结果"""
    final_answer: str
    agent_name: str = ""
    steps: List[AgentStep] = field(default_factory=list)
    total_steps: int = 0
    tools_used: List[str] = field(default_factory=list)
    options: List[Dict[str, str]] = field(default_factory=list)
    graph_data: Optional[Dict] = None


@dataclass
class Tool:
    """Agent 工具"""
    name: str
    description: str
    parameters: str
    func: Callable[..., Coroutine[Any, Any, str]]


class ToolRegistry:
    """工具注册表"""

    def __init__(self):
        self._tools: Dict[str, Tool] = {}

    def register(self, name: str, description: str, parameters: str,
                 func: Callable[..., Coroutine[Any, Any, str]]):
        self._tools[name] = Tool(name=name, description=description,
                                  parameters=parameters, func=func)

    def get(self, name: str) -> Optional[Tool]:
        return self._tools.get(name)

    def list_tools(self) -> List[Tool]:
        return list(self._tools.values())

    def format_descriptions(self) -> str:
        lines = []
        for t in self._tools.values():
            lines.append(f"- {t.name}: {t.description}")
            lines.append(f"  参数: {t.parameters}")
        return "\n".join(lines)


class BaseAgent(ABC):
    """所有 Agent 的抽象基类"""

    def __init__(self, name: str, llm_client, tools: ToolRegistry = None):
        self.name = name
        self.llm = llm_client
        self.tools = tools or ToolRegistry()
        self.conversation_history: List[Dict] = []

    @abstractmethod
    def get_system_prompt(self) -> str:
        """每个 Agent 定义自己的角色和规则"""
        pass

    @abstractmethod
    def should_handle(self, user_input: str, context: dict = None) -> bool:
        """判断该 Agent 是否应该处理这个问题"""
        pass

    async def run(
        self,
        query: str,
        chat_history: Optional[List[Dict]] = None,
        on_step: Optional[Callable[[AgentStep], Coroutine[Any, Any, None]]] = None,
    ) -> AgentResult:
        """
        核心对话入口。子类可覆盖此方法实现自定义逻辑。
        默认实现：直接调用 LLM 生成回答。
        """
        try:
            messages = [
                {"role": "system", "content": self.get_system_prompt()},
            ]
            if chat_history:
                for msg in chat_history[-10:]:
                    messages.append(msg)
            messages.append({"role": "user", "content": query})

            answer = await self.llm.chat(messages, max_tokens=1500, temperature=0.3)
            return AgentResult(
                final_answer=answer,
                agent_name=self.name,
                total_steps=0,
            )
        except Exception as e:
            logger.error("[Agent:%s] 执行失败: %s", self.name, e)
            return AgentResult(
                final_answer=f"抱歉，{self.name}处理您的问题时遇到了技术问题，请稍后重试。",
                agent_name=self.name,
            )

    async def _execute_tool(self, action: str, action_input: Any,
                            timeout: float = 10.0) -> str:
        """执行工具调用（含重试 + 超时）"""
        tool = self.tools.get(action)
        if tool is None:
            return f"错误：工具 '{action}' 不存在。可用：{', '.join(t.name for t in self.tools.list_tools())}"

        params = action_input if isinstance(action_input, dict) else {"raw": str(action_input)}
        last_error = None
        for attempt in range(3):
            try:
                start = time.time()
                result = await asyncio.wait_for(tool.func(**params), timeout=timeout)
                latency_ms = (time.time() - start) * 1000
                logger.debug("[Tool] %s 完成 %.0fms (attempt %d)", action, latency_ms, attempt + 1)
                if len(result) > 2000:
                    result = result[:2000] + "\n...(已截断)"
                return result
            except asyncio.TimeoutError:
                last_error = f"工具 '{action}' 超时（{timeout}s）"
                logger.warning("[Tool] %s 超时 (attempt %d)", action, attempt + 1)
            except TypeError as e:
                return f"工具参数错误：{e}"
            except Exception as e:
                last_error = f"工具 '{action}' 执行失败：{e}"
                logger.warning("[Tool] %s 失败 (attempt %d): %s", action, attempt + 1, e)
            if attempt < 2:
                await asyncio.sleep(0.5 * (attempt + 1))
        return last_error
