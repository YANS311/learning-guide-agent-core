"""A small learning assistant using fictional courses and optional model access."""

from __future__ import annotations

import json
import os
import time
from dataclasses import asdict
from pathlib import Path
from uuid import uuid4

from dotenv import load_dotenv
from fastapi import FastAPI
from fastapi.responses import StreamingResponse
from pydantic import BaseModel, Field

from engines import AgentDispatcher, AgentResult, BaseAgent, ToolRegistry
from engines.agent_trace import TraceCollector, TraceRecord, ToolCallRecord

load_dotenv(Path(__file__).resolve().parents[1] / ".env")

# Entirely fictional demonstration data, independent of any curriculum.
COURSES = {
    "编程入门": {"prerequisites": [], "goal": "编写简单的程序"},
    "数据结构": {"prerequisites": ["编程入门"], "goal": "选择和使用基本数据结构"},
    "算法实践": {"prerequisites": ["数据结构"], "goal": "分析复杂度并实现基础算法"},
}


async def course_lookup(course: str) -> str:
    info = COURSES.get(course)
    return json.dumps({"course": course, "info": info}, ensure_ascii=False)


class ChatRequest(BaseModel):
    message: str = Field(min_length=1, max_length=2000)


class LearningAgent(BaseAgent):
    def __init__(self, llm_client=None):
        tools = ToolRegistry()
        tools.register("course_lookup", "查询示例课程", '{"course": "课程名称"}', course_lookup)
        super().__init__("learning_guide", llm_client, tools)

    def get_system_prompt(self) -> str:
        return "你是通用学习助手。用简明步骤解释学习方法，不编造课程政策。"

    def should_handle(self, user_input: str, context: dict = None) -> bool:
        return bool(user_input.strip())

    async def run(self, query: str, chat_history=None, on_step=None) -> AgentResult:
        for name in COURSES:
            if name in query:
                result = await self._execute_tool("course_lookup", {"course": name})
                info = json.loads(result)["info"]
                prerequisites = "、".join(info["prerequisites"]) or "无先修要求"
                return AgentResult(
                    final_answer=f"示例课程：{name}。先修：{prerequisites}。目标：{info['goal']}。",
                    agent_name=self.name,
                    tools_used=["course_lookup"],
                )
        if self.llm is not None:
            return await super().run(query, chat_history, on_step)
        return AgentResult(
            final_answer="这是离线学习示例。可询问编程入门、数据结构或算法实践的先修关系。",
            agent_name=self.name,
        )


def create_app(llm_client=None) -> FastAPI:
    app = FastAPI(title="通用智能导学 Agent 示例", version="0.1.0")
    dispatcher = AgentDispatcher()
    dispatcher.register(LearningAgent(llm_client))
    traces = TraceCollector(max_records=100)
    app.state.dispatcher = dispatcher
    app.state.traces = traces

    async def answer(message: str) -> AgentResult:
        start = time.perf_counter()
        result = await dispatcher.dispatch(message)
        traces.record(TraceRecord(
            request_id=uuid4().hex,
            query="",  # Example metrics intentionally retain no user message.
            intent="LEARNING",
            intent_confidence=1.0,
            route="model" if llm_client and not result.tools_used else "demo",
            tool_calls=[ToolCallRecord(name, {}, "", 0) for name in result.tools_used],
            total_steps=result.total_steps,
            total_latency_ms=(time.perf_counter() - start) * 1000,
        ))
        return result

    @app.get("/")
    async def index():
        return {"name": app.title, "docs": "/docs", "mode": "model" if llm_client else "offline_demo"}

    @app.get("/api/health")
    async def health():
        return {"status": "ok", "mode": "model" if llm_client else "offline_demo"}

    @app.get("/api/courses")
    async def courses():
        return {"synthetic": True, "courses": COURSES}

    @app.post("/api/chat")
    async def chat(body: ChatRequest):
        return asdict(await answer(body.message))

    @app.post("/api/chat/stream")
    async def stream(body: ChatRequest):
        # Event delivery, not token-by-token model generation.
        async def events():
            yield 'event: status\ndata: {"status":"processing"}\n\n'
            result = await answer(body.message)
            yield "event: answer\ndata: " + json.dumps(asdict(result), ensure_ascii=False) + "\n\n"
            yield 'event: done\ndata: {}\n\n'
        return StreamingResponse(events(), media_type="text/event-stream")

    @app.get("/api/metrics")
    async def metrics():
        return traces.get_stats()

    return app


def configured_app() -> FastAPI:
    if os.getenv("LLM_ENABLED", "false").lower() == "true":
        from engines.llm_client import LLMClient
        return create_app(LLMClient())
    return create_app()


app = configured_app()
