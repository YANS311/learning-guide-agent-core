"""Deterministic analysis of a validated prerequisite snapshot."""
from __future__ import annotations

import hashlib
import json
from dataclasses import dataclass
from time import monotonic
from typing import Annotated, Literal

import networkx as nx
from pydantic import BaseModel, ConfigDict, Field, StringConstraints

MAX_NODES = 2500
MAX_EDGES = 12000
MAX_DEPTH = 32
MAX_PATHS = 40
CourseID = Annotated[str, StringConstraints(min_length=1, max_length=64, pattern=r"^[A-Za-z0-9][A-Za-z0-9_.-]*$")]
Title = Annotated[str, StringConstraints(strip_whitespace=True, min_length=1, max_length=160)]
Policy = Literal["required", "all"]


class AnalysisError(Exception):
    def __init__(self, status: int, code: str):
        super().__init__(code)
        self.status, self.code = status, code


class Input(BaseModel):
    model_config = ConfigDict(extra="forbid", frozen=True)


class Course(Input):
    id: CourseID
    name: Title
    semester: str = Field(default="", max_length=24)
    credits: float = Field(default=0, ge=0, le=100, allow_inf_nan=False)


class Relation(Input):
    source: CourseID
    target: CourseID
    strength: Literal["strong", "weak"] = "strong"
    reason: str = Field(default="", max_length=512)


class Dataset(Input):
    courses: tuple[Course, ...] = Field(min_length=1, max_length=MAX_NODES)
    relations: tuple[Relation, ...] = Field(max_length=MAX_EDGES)


@dataclass(frozen=True)
class Snapshot:
    version: str
    courses: dict[str, Course]
    all: nx.DiGraph
    required: nx.DiGraph
    duplicates: int

    @classmethod
    def build(cls, raw: Dataset) -> Snapshot:
        courses: dict[str, Course] = {}
        edges: dict[tuple[str, str], Relation] = {}
        duplicates = 0
        for c in sorted(raw.courses, key=lambda x: x.id):
            if c.id in courses:
                raise AnalysisError(503, "duplicate_course")
            courses[c.id] = c
        for r in sorted(raw.relations, key=lambda x: (x.source, x.target)):
            key = (r.source, r.target)
            if any(v not in courses for v in key):
                raise AnalysisError(503, "dangling_relation")
            if key in edges:
                if edges[key] != r:
                    raise AnalysisError(503, "ambiguous_relation")
                duplicates += 1
            edges[key] = r
        graph = nx.DiGraph()
        graph.add_nodes_from(courses)
        graph.add_edges_from((r.source, r.target, {"relation": r}) for r in edges.values())
        required = nx.DiGraph()
        required.add_nodes_from(courses)
        required.add_edges_from((r.source, r.target, {"relation": r}) for r in edges.values() if r.strength == "strong")
        material = Dataset(courses=tuple(courses.values()), relations=tuple(edges.values())).model_dump(mode="json")
        digest = hashlib.sha256(json.dumps(material, sort_keys=True, ensure_ascii=False).encode()).hexdigest()
        return cls(digest, courses, nx.freeze(graph), nx.freeze(required), duplicates)

    def require(self, *ids: str):
        if any(i not in self.courses for i in ids):
            raise AnalysisError(404, "course_not_found")

    def graph(self, policy: Policy):
        return self.required if policy == "required" else self.all


def check(deadline: float) -> None:
    if monotonic() >= deadline:
        raise AnalysisError(504, "query_timeout")


def tree(graph: nx.DiGraph, target: str, deadline: float, *, backwards=True):
    """Keep one deterministic shortest-path predecessor, not all DAG paths."""
    parent = {target: None}
    distance = {target: 0}
    for start, end in nx.bfs_edges(graph, target, reverse=backwards):
        check(deadline)
        parent[end] = start
        distance[end] = distance[start] + 1
    return parent, distance


def route(parent, node, depth, max_depth):
    if depth > max_depth:
        return None
    path = [node]
    while parent[path[-1]] is not None:
        path.append(parent[path[-1]])
    return path


def ordered(graph):
    try:
        return list(nx.lexicographical_topological_sort(graph, key=str))
    except nx.NetworkXUnfeasible:
        raise AnalysisError(409, "dependency_cycle") from None


def depth_info(graph, sequence, deadline):
    depth = {}
    for node in sequence:
        check(deadline)
        depth[node] = 1 + max((depth[p] for p in graph.predecessors(node)), default=-1)
    return depth


class Analysis:
    def __init__(self, snapshot: Snapshot, policy: Policy, deadline: float):
        self.snapshot, self.graph, self.deadline = snapshot, snapshot.graph(policy), deadline

    def closure(self, course):
        self.snapshot.require(course)
        parent, levels = tree(self.graph, course, self.deadline)
        sub = self.graph.subgraph(levels)
        sequence = ordered(sub)
        depth = depth_info(sub, sequence, self.deadline)
        return parent, levels, sub, sequence, depth

    def prerequisites(self, course, path_limit=12, max_depth=MAX_DEPTH):
        parent, levels, sub, _, depth = self.closure(course)
        all_ids = sorted(set(levels) - {course}, key=lambda n: (levels[n], n))
        paths = [{"course_id": n, "path": route(parent, n, levels[n], max_depth)}
                 for n in all_ids[:path_limit] if levels[n] <= max_depth]
        return {
            "course": self.snapshot.courses[course],
            "direct": [self.snapshot.courses[n] for n in sorted(sub.predecessors(course))],
            "all_prerequisites": [self.snapshot.courses[n] for n in all_ids],
            "levels": [{"course": self.snapshot.courses[n], "distance": levels[n], "column": depth[course]-depth[n]} for n in all_ids],
            "depth": depth[course], "paths": paths, "paths_truncated": len(paths) < len(all_ids),
            "relations": [sub.edges[a, b]["relation"] for a, b in sorted(sub.edges())],
        }

    def learning_order(self, course):
        _, _, _, sequence, depth = self.closure(course)
        return {"course_id": course, "sequence": [self.snapshot.courses[n] for n in sequence],
                "stages": [{"course_id": n, "stage": depth[n] + 1} for n in sequence]}

    def compare(self, left, right, path_limit=12, max_depth=MAX_DEPTH):
        lp, ld, lg, _, ldepth = self.closure(left)
        rp, rd, rg, _, rdepth = self.closure(right)
        la, ra = set(ld) - {left}, set(rd) - {right}
        common = sorted(la & ra)
        paths = [{"course_id": n, "left": route(lp, n, ld[n], max_depth),
                  "right": route(rp, n, rd[n], max_depth)}
                 for n in common[:path_limit] if max(ld[n], rd[n]) <= max_depth]
        edge_l, edge_r = set(lg.edges()), set(rg.edges())
        course_list = lambda ids: [self.snapshot.courses[n] for n in ids]
        return {"left": left, "right": right, "depths": [ldepth[left], rdepth[right]],
                "common": course_list(common), "left_only": course_list(sorted(la - ra)),
                "right_only": course_list(sorted(ra - la)), "common_paths": paths,
                "paths_truncated": len(paths) < len(common),
                "structure": {"shared_edges": len(edge_l & edge_r), "left_only_edges": len(edge_l - edge_r),
                              "right_only_edges": len(edge_r - edge_l)}}

    def missing(self, course, completed):
        self.snapshot.require(course, *completed)
        _, levels, sub, _, _ = self.closure(course)
        direct = set(sub.predecessors(course))
        completed = set(completed)
        return {"course_id": course, "satisfied_direct": sorted(direct & completed),
                "missing_direct": sorted(direct - completed),
                "missing_indirect": sorted(set(levels) - direct - {course} - completed)}

    def dependents(self, course):
        self.snapshot.require(course)
        parent, levels = tree(self.graph, course, self.deadline, backwards=False)
        ordered(self.graph.subgraph(levels))
        downstream = sorted(set(levels) - {course}, key=lambda n: (levels[n], n))
        direct = {n for n in downstream if levels[n] == 1}
        return {"course_id": course, "direct": [self.snapshot.courses[n] for n in downstream if n in direct],
                "indirect": [self.snapshot.courses[n] for n in downstream if n not in direct],
                "impact_count": len(downstream),
                "paths": [{"course_id": n, "path": route(parent, n, levels[n], MAX_DEPTH)[::-1]}
                          for n in downstream[:MAX_PATHS] if levels[n] <= MAX_DEPTH]}
