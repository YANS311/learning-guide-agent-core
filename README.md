# Learning Guide Agent Core · 通用智能导学组件

从智能导学项目中提取的通用 Python Agent 组件，包含 Agent 基类、工具注册与调用、按注册顺序匹配的调度器、OpenAI 兼容模型客户端，以及内存运行统计。附带一个使用虚构课程的 FastAPI 示例，可直接在离线模式运行。

这是通用组件子集，适合学习、复用与二次开发。示例数据不对应任何学校、专业培养方案或真实人员。

## 组件

| 文件 | 用途 |
| --- | --- |
| `engines/base_agent.py` | Agent 接口、结果结构、工具注册、工具超时与重试 |
| `engines/agent_dispatcher.py` | 按注册顺序路由请求与错误兜底 |
| `engines/llm_client.py` | OpenAI 兼容模型调用、超时、重试与 JSON 解析 |
| `engines/agent_trace.py` | 有限条数的内存记录、延迟与工具调用统计 |
| `examples/app.py` | 虚构课程查询、可选模型问答与 SSE 事件接口 |

## 快速运行

需要 Python 3.10 或更新版本。在仓库根目录执行：

```bash
python -m venv .venv
# Windows PowerShell
.venv\Scripts\Activate.ps1
# macOS / Linux: source .venv/bin/activate
python -m pip install -r requirements.txt
python -m uvicorn examples.app:app --host 127.0.0.1 --port 8000
```

打开 `http://127.0.0.1:8000/docs`，在 `POST /api/chat` 输入：

```json
{"message": "数据结构有什么先修要求？"}
```

默认离线模式直接查询三个虚构课程，不需要模型、数据库、文档索引或密钥。

## 可选模型配置

将 `.env.example` 复制为 `.env`，设置 `LLM_ENABLED=true`，并填写模型服务的 URL、模型名称及必要的凭据。客户端使用 OpenAI SDK 的兼容聊天接口；不同服务的参数支持情况需要自行验证。课程查询仍通过示例工具完成，其他问题才发送至配置的模型服务。

`.env` 仅保留在本地。示例应用只从本仓库根目录加载该文件。

## 接口

| 方法与路径 | 功能 |
| --- | --- |
| `GET /api/health` | 应用存活与当前模式；不检测远程模型可用性 |
| `GET /api/courses` | 明确标记为虚构的课程数据 |
| `POST /api/chat` | 课程查询或模型问答 |
| `POST /api/chat/stream` | `status`、`answer`、`done` 三种 SSE 事件 |
| `GET /api/metrics` | 当前进程中的请求数、路由数、延迟与工具调用数 |

SSE 示例发送状态与完整回答事件，未实现模型逐 token 输出。统计不会加载原项目评测报告，也不宣称未经验证的准确率；示例不记录用户问题或回答内容。原始 `TraceCollector` 可供调用方自行记录数据，请自行决定记录内容与保留方式。

## 验证

```bash
python -m pip install -r requirements-dev.txt
python -m pytest -q
```

测试覆盖离线课程查询、工具失败后的兜底、路由失败恢复、真实累计统计、输入校验及 SSE 事件。

## Docker

```bash
docker build -t learning-guide-agent-core .
docker run --rm -p 127.0.0.1:8000:8000 learning-guide-agent-core
```

这是开发示例，未包含用户认证、持久会话、生产限流或管理后台。示例工具仅执行只读查询；复用自动重试机制调用有副作用的工具时，应实现幂等性。

导出范围与脱敏说明见 [docs/PUBLIC_EXPORT.md](docs/PUBLIC_EXPORT.md)。
