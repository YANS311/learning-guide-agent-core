"""
agent_trace.py
Agent 可观测性 — 记录每次请求的完整 Trace
"""

from __future__ import annotations


import logging

import time
from dataclasses import dataclass, field, asdict
from typing import Any, Dict, List, Optional

logger = logging.getLogger("AgentTrace")


@dataclass
class ToolCallRecord:
    tool_name: str
    params: Any
    result: str
    latency_ms: float


@dataclass
class TraceRecord:
    request_id: str
    query: str
    intent: str
    intent_confidence: float
    route: str  # fast_path / react / rule / chat / clarification
    tool_calls: List[ToolCallRecord] = field(default_factory=list)
    final_answer: str = ""
    total_steps: int = 0
    total_latency_ms: float = 0
    llm_model: str = ""
    error: Optional[str] = None
    timestamp: float = field(default_factory=time.time)

    def to_dict(self) -> Dict[str, Any]:
        d = asdict(self)
        d["timestamp_iso"] = time.strftime("%Y-%m-%d %H:%M:%S", time.localtime(self.timestamp))
        return d


class TraceCollector:
    """收集和存储 Agent Trace"""

    def __init__(self, max_records: int = 1000):
        self.records: List[TraceRecord] = []
        self.max_records = max_records

    def record(self, trace: TraceRecord):
        self.records.append(trace)
        if len(self.records) > self.max_records:
            self.records = self.records[-self.max_records:]

        # 同时写入日志
        logger.info(
            "[Trace] id=%s intent=%s route=%s steps=%d latency=%.0fms tools=%s",
            trace.request_id, trace.intent, trace.route,
            trace.total_steps, trace.total_latency_ms,
            ",".join(tc.tool_name for tc in trace.tool_calls),
        )

    def get_recent(self, n: int = 50) -> List[Dict]:
        return [r.to_dict() for r in self.records[-n:]]

    def get_stats(self) -> Dict[str, Any]:
        intent_counts = {}
        route_counts = {}
        tool_counts = {}
        total_latency = 0
        errors = 0

        for r in self.records:
            if r.intent:
                intent_counts[r.intent] = intent_counts.get(r.intent, 0) + 1
            route_counts[r.route] = route_counts.get(r.route, 0) + 1
            total_latency += r.total_latency_ms
            if r.error:
                errors += 1

            # 统计工具调用频次
            if hasattr(r, "tools_used") and r.tools_used:
                for t in r.tools_used:
                    tool_counts[t] = tool_counts.get(t, 0) + 1
            elif hasattr(r, "tool_calls") and r.tool_calls:
                for tc in r.tool_calls:
                    name = tc.tool_name if hasattr(tc, "tool_name") else str(tc)
                    tool_counts[name] = tool_counts.get(name, 0) + 1

        avg_lat = round(total_latency / max(len(self.records), 1), 1)

        stats = {
            "total_requests": len(self.records),
            "by_intent": intent_counts,
            "by_route": route_counts,
            "tool_usage_counts": tool_counts,
            "avg_latency_ms": avg_lat,
            "error_count": errors,
        }

        return stats


# 全局实例
trace_collector = TraceCollector()
