import asyncio
import os

os.environ["LLM_ENABLED"] = "false"

from fastapi.testclient import TestClient

from engines import AgentDispatcher
from engines.agent_trace import TraceCollector, TraceRecord
from examples.app import LearningAgent, create_app


def test_offline_course_query_and_private_metrics():
    client = TestClient(create_app())
    assert client.get("/api/health").json()["mode"] == "offline_demo"
    result = client.post("/api/chat", json={"message": "数据结构有什么先修要求？"})
    assert result.status_code == 200
    assert "编程入门" in result.json()["final_answer"]
    assert result.json()["tools_used"] == ["course_lookup"]
    metrics = client.get("/api/metrics").json()
    assert metrics["total_requests"] == 1
    assert metrics["tool_usage_counts"] == {"course_lookup": 1}
    assert "intent_accuracy" not in metrics
    assert client.app.state.traces.records[0].query == ""
    assert client.app.state.traces.records[0].final_answer == ""


def test_empty_metrics_do_not_claim_benchmark_results():
    metrics = TestClient(create_app()).get("/api/metrics").json()
    assert metrics["total_requests"] == 0
    assert metrics["avg_latency_ms"] == 0
    assert "intent_accuracy" not in metrics


def test_input_validation_and_unknown_course_fallback():
    client = TestClient(create_app())
    for invalid in ["", "x" * 2001]:
        assert client.post("/api/chat", json={"message": invalid}).status_code == 422
    answer = client.post("/api/chat", json={"message": "怎样安排学习时间？"}).json()
    assert "离线学习示例" in answer["final_answer"]
    assert client.get("/api/courses").json()["synthetic"] is True


def test_sse_delivers_status_answer_and_completion():
    response = TestClient(create_app()).post("/api/chat/stream", json={"message": "算法实践先修"})
    assert response.status_code == 200
    assert response.headers["content-type"].startswith("text/event-stream")
    assert response.text.index("event: status") < response.text.index("event: answer")
    assert response.text.index("event: answer") < response.text.index("event: done")
    assert "数据结构" in response.text


def test_tool_retries_after_transient_failure():
    agent = LearningAgent()
    attempts = []

    async def transient(**params):
        attempts.append(params)
        if len(attempts) == 1:
            raise ConnectionError("temporary")
        return "recovered"

    agent.tools.register("transient", "test", "{}", transient)
    assert asyncio.run(agent._execute_tool("transient", {})) == "recovered"
    assert len(attempts) == 2


def test_dispatcher_skips_broken_matcher():
    class BrokenMatcher(LearningAgent):
        def should_handle(self, user_input, context=None):
            raise RuntimeError("matcher failed")

    dispatcher = AgentDispatcher()
    dispatcher.register(BrokenMatcher())
    dispatcher.register(LearningAgent())
    assert "编程入门" in asyncio.run(dispatcher.dispatch("数据结构")).final_answer


def test_model_failure_returns_generic_answer():
    class BrokenModel:
        async def chat(self, *args, **kwargs):
            raise RuntimeError("private provider error")

    answer = TestClient(create_app(BrokenModel())).post("/api/chat", json={"message": "如何复习？"}).json()
    assert "技术问题" in answer["final_answer"]
    assert "private provider error" not in answer["final_answer"]


def test_trace_records_are_bounded():
    collector = TraceCollector(max_records=2)
    for index in range(3):
        collector.record(TraceRecord(str(index), "", "LEARNING", 1.0, "demo"))
    assert [record.request_id for record in collector.records] == ["1", "2"]
    assert collector.get_stats()["total_requests"] == 2
