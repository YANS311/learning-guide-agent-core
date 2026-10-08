"""
agent_dispatcher.py
Agent 调度器 — 负责分发请求到合适的 Agent

路由策略：
1. 按注册顺序遍历所有 Agent
2. 调用 agent.should_handle() 判断是否处理
3. 第一个匹配的 Agent 负责处理
4. 无匹配时返回兜底消息
"""

from __future__ import annotations

import logging
from typing import Dict, List, Optional, Tuple

from .base_agent import BaseAgent, AgentResult

logger = logging.getLogger("AgentDispatcher")


class AgentDispatcher:
    """Agent 调度器"""

    def __init__(self):
        self.agents: List[BaseAgent] = []
        self._fallback_message = (
            "### 结论\n\n抱歉，我暂时无法回答这个问题。\n\n"
            "### 下一步建议\n\n"
            "- 可以尝试问我关于选课、先修关系、课程信息、能力培养等问题\n"
            "- 也可以尝试补充学习目标和当前基础"
        )

    def register(self, agent: BaseAgent):
        """注册一个 Agent"""
        self.agents.append(agent)
        logger.info("[Dispatcher] 注册 Agent: %s", agent.name)

    def get_agent(self, name: str) -> Optional[BaseAgent]:
        """按名称获取 Agent"""
        for agent in self.agents:
            if agent.name == name:
                return agent
        return None

    def list_agents(self) -> List[Dict]:
        """列出所有已注册的 Agent"""
        return [{"name": a.name, "type": type(a).__name__} for a in self.agents]

    def find_agent(self, user_input: str, context: dict = None) -> Optional[BaseAgent]:
        """找到最适合处理该请求的 Agent"""
        for agent in self.agents:
            try:
                if agent.should_handle(user_input, context):
                    logger.info("[Dispatcher] 路由到 Agent: %s", agent.name)
                    return agent
            except Exception as e:
                logger.warning("[Dispatcher] Agent %s.should_handle() 异常: %s", agent.name, e)
        return None

    async def dispatch(
        self,
        user_input: str,
        chat_history: Optional[List[Dict]] = None,
        context: dict = None,
        on_step=None,
    ) -> AgentResult:
        """
        分发请求到合适的 Agent。

        Returns:
            AgentResult — 包含 agent_name 字段标识由哪个 Agent 处理
        """
        agent = self.find_agent(user_input, context)
        if agent is None:
            logger.info("[Dispatcher] 无匹配 Agent，返回兜底消息")
            return AgentResult(
                final_answer=self._fallback_message,
                agent_name="none",
            )

        try:
            result = await agent.run(
                query=user_input,
                chat_history=chat_history,
                on_step=on_step,
            )
            result.agent_name = agent.name
            return result
        except Exception as e:
            logger.error("[Dispatcher] Agent %s 执行失败: %s", agent.name, e)
            return AgentResult(
                final_answer=f"### 结论\n\n处理您的问题时遇到了技术问题，请稍后重试。",
                agent_name=agent.name,
            )
