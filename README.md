# Learning Guide Agent Core · 通用智能导学组件

智能导学项目的 **Python、Java、Go 三语言脱敏示例**。Python 版提取了通用 Agent 组件；Java、Go 版按同一公开示例的功能重新实现，包含 Agent 接口、工具调用与重试、按注册顺序匹配的调度器、可选 OpenAI 兼容模型客户端，以及内存运行统计。三版均使用相同的虚构课程，可在离线模式运行。

这是通用组件子集，适合学习、复用与二次开发。示例数据不对应任何学校、专业培养方案或真实人员。Java、Go 版是独立的示例实现，未迁移原系统的全部图谱、RAG、管理后台或专业业务规则。

## 三语言版本

| 版本 | 位置 | 环境 | HTTP 实现 | 默认端口 |
| --- | --- | --- | --- | --- |
| Python | `engines/`、`examples/` | Python 3.10+ | FastAPI | 8000 |
| Java | [java/](java/) | JDK 17+、Maven 3.9+ | JDK HttpServer；Jackson 解析 JSON | 8081 |
| Go | [go/](go/) | Go 1.22+ | 标准库 `net/http`，无第三方模块依赖 | 8082 |

三版均提供课程查询、离线兜底、可选模型问答、工具超时与重试、SSE 事件和内存统计。默认运行时无需密钥或外部服务，服务绑定本机回环地址。

共同响应字段为 `final_answer`、`agent_name`、`total_steps`、`tools_used`；Python 另外保留扩展结果字段。Java、Go 输入必须是包含 `message` 字符串的 JSON 对象，非空白且不超过 2000 个 Unicode 字符，请求体上限为 16 KiB；Python 使用 Pydantic 的 1–2000 字符校验。错误统一返回通用提示，示例统计不保存问题或回答。

Python 示例统计最近 100 条记录，Java、Go 统计当前进程启动后的全部请求；进程重启后统计清零。Java、Go 同时累计失败数。三版 SSE 都发送完整回答事件，未实现模型逐 token 输出。

## Java 快速运行

在仓库根目录执行：

```bash
cd java
mvn clean verify
java -jar target/learning-guide-java-0.1.0.jar
```

默认地址为 `http://127.0.0.1:8081`。构建会运行 JUnit 集成测试，并生成可独立执行的 JAR；首次构建需要联网获取 Maven 插件及 Java 依赖。

## Go 快速运行

在仓库根目录执行：

```bash
cd go
go test ./...
go vet ./...
go run ./cmd/server
```

默认地址为 `http://127.0.0.1:8082`。也可执行 `go build -o guide-server ./cmd/server` 构建可执行文件。Go 版仅依赖标准库。

用下面的请求测试 Java 或 Go，把端口替换为对应版本：

```bash
curl -X POST http://127.0.0.1:8081/api/chat -H "Content-Type: application/json" -d '{"message":"数据结构有什么先修要求？"}'
```

Windows PowerShell 可使用：

```powershell
$body = @{message="数据结构有什么先修要求？"} | ConvertTo-Json
Invoke-RestMethod -Method Post -Uri http://127.0.0.1:8081/api/chat -ContentType "application/json; charset=utf-8" -Body ([Text.Encoding]::UTF8.GetBytes($body))
```

Java、Go 版通过进程环境变量配置，**不会自动读取 `.env`**。例如 PowerShell：

```powershell
$env:PORT="8081" # Go 可使用 8082
$env:LLM_ENABLED="true"
$env:LLM_BASE_URL="http://localhost:11434/v1"
$env:LLM_API_KEY="ollama" # 本地模型占位值，远程服务请使用自己的凭据
$env:LLM_MODEL="qwen2.5:7b"
```

设置后重新运行对应启动命令；macOS/Linux 可使用 `export NAME=value` 设置相同变量。非课程问题才会发送到模型服务。Java、Go 的模型请求超时为 10 秒，最多尝试两次；对 HTTP 429、5xx 或连接失败重试，对其他非成功状态直接失败。Python 客户端对 4xx（含 429）不重试。不同提供方对 `max_completion_tokens` 等兼容参数的支持需自行验证；测试使用本机模拟提供方，不调用真实远程模型。

## 脱敏说明

三语言版本只发布通用代码和虚构示例，不包含学校或个人信息、培养方案、原图谱数据、教学文件、评测资产、截图、真实 `.env`、密钥、管理员凭据、数据库、向量索引、日志或原项目 Git 历史。Java、Go 从公开示例重新实现，未复制原私有文件。构建产物和本地配置由 `.gitignore` 排除。

完整范围见 [docs/PUBLIC_EXPORT.md](docs/PUBLIC_EXPORT.md)。

## Python 组件

| 文件 | 用途 |
| --- | --- |
| `engines/base_agent.py` | Agent 接口、结果结构、工具注册、工具超时与重试 |
| `engines/agent_dispatcher.py` | 按注册顺序路由请求与错误兜底 |
| `engines/llm_client.py` | OpenAI 兼容模型调用、超时、重试与 JSON 解析 |
| `engines/agent_trace.py` | 有限条数的内存记录、延迟与工具调用统计 |
| `examples/app.py` | 虚构课程查询、可选模型问答与 SSE 事件接口 |

## Python 快速运行

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

## Python 可选模型配置

将 `.env.example` 复制为 `.env`，设置 `LLM_ENABLED=true`，并填写模型服务的 URL、模型名称及必要的凭据。客户端使用 OpenAI SDK 的兼容聊天接口；不同服务的参数支持情况需要自行验证。课程查询仍通过示例工具完成，其他问题才发送至配置的模型服务。

`.env` 仅保留在本地。示例应用只从本仓库根目录加载该文件。

## 接口

| 方法与路径 | 功能 |
| --- | --- |
| `GET /api/health` | 三版均提供：应用存活与当前模式；不检测远程模型可用性 |
| `GET /api/courses` | 明确标记为虚构的课程数据 |
| `POST /api/chat` | 课程查询或模型问答 |
| `POST /api/chat/stream` | `status`、`answer`、`done` 三种 SSE 事件 |
| `GET /api/metrics` | 请求数、路由数、延迟与工具调用数；统计窗口见上文 |

SSE 示例发送状态与完整回答事件，未实现模型逐 token 输出。统计不会加载原项目评测报告，也不宣称未经验证的准确率；示例不记录用户问题或回答内容。原始 `TraceCollector` 可供调用方自行记录数据，请自行决定记录内容与保留方式。

## Python 验证

```bash
python -m pip install -r requirements-dev.txt
python -m pytest -q
```

测试覆盖离线课程查询、工具失败后的兜底、路由失败恢复、真实累计统计、输入校验及 SSE 事件。

## Python Docker

```bash
docker build -t learning-guide-agent-core .
docker run --rm -p 127.0.0.1:8000:8000 learning-guide-agent-core
```

三版都是开发示例，未包含用户认证、持久会话、生产限流或管理后台。示例工具仅执行只读查询；复用自动重试机制调用有副作用的工具时，应实现幂等性。Go 工具函数应遵守传入 context 的取消信号，Java 工具应响应线程中断；超时不能强制终止不配合的自定义工具。

导出范围与脱敏说明见 [docs/PUBLIC_EXPORT.md](docs/PUBLIC_EXPORT.md)。
