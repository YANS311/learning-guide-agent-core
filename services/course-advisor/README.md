# 智能选课导学 Agent 系统 V1.0

Go 负责候选排序、约束核验、SQLite 业务状态、异步任务、SSE 与中文页面。它通过环回 HTTP 读取 Phase 4 课程目录和必需先修，不写课程图谱。不启动 Python RAG 或模型也可以生成、保存和复查方案。

## 运行

在相邻的 `prerequisite-analysis` 目录按其说明启动服务（例如端口 8094）。从本目录用 Go 1.25 或兼容版本执行：

```sh
go run . --db ./local/course-advisor.sqlite3 --prereq http://127.0.0.1:8094 --sample-evidence
```

浏览器访问 `http://127.0.0.1:8095/`。示例课程和证据为合成资料，内置证据只与匹配的合成课程快照配合。用授权本地数据时，去掉 `--sample-evidence`，用 `--evidence` 指定同结构的证据 JSON；不得放入公开工作区。数据库文件应放在私有目录，应用拒绝未标记的其他数据库；这是本机工作区凭证，不是线上统一身份。任务状态、画像、历史和事件写入该文件，须作为私有资料保留。

### 可选检索

```sh
python -m pip install -r rag/requirements.txt
python rag/server.py --evidence testdata/handbook.json
```

启动 Go 时增加 `--rag http://127.0.0.1:8195`。Python sidecar 可选使用环境中已安装的 FAISS、sentence-transformers 与可信本地 safetensors 模型目录（`--embedding-dir`），在内存构建索引并做 BM25、向量与 RRF，不读未知 pickle、不访问网络。Go 停用或无法连接 sidecar 时使用本地证据。不启用任何可选项时，不会调用远程模型。可另用显式 `--ollama` 和 `--model` 启用环回 Ollama，只传固定说明样式请求；模型不会接收学生、课程或文档数据，也不产生可执行工具调用。

## 接口

`POST /api/v1/workspaces` 建立本机工作区。其余业务接口使用 Bearer 凭证：`GET/PUT /api/v1/profile` 读取或条件更新画像，`GET /api/v1/courses` 检索工作目录，`POST /api/v1/runs` 使用 `Idempotency-Key` 排队，`GET /api/v1/runs` 分页查询历史，`GET /api/v1/runs/{id}` 查询任务与版本，`POST /api/v1/runs/{id}/cancel` 取消。使用 `GET /api/v1/runs/{id}/events` 接收工作流 SSE；携带 `Last-Event-ID` 可重放后续持久事件。事件不是逐 Token 输出。

缺少正式排课、名额与学籍政策数据时，结果保持待确认。阶段、方向和偏好不被冒充为学校硬约束；必需先修、已修过滤、用户填写的学分上限和门数由程序核验。不求全局最优。重启后未完成任务标为中断，可用新幂等键显式重试，不做后台自动重跑。

## 测试

```sh
go test -race ./...
go vet ./...
go build -o /tmp/course-advisor .
node tests/browser.cjs /tmp/course-advisor python
```

浏览器测试需要 Playwright Chromium 与 Phase 4 Python 依赖；自动启动独立合成课程服务及临时真实 SQLite 数据库。Go 服务无需接入 Redis、MySQL、Docker 或任意真实教学库。

## Phase 6 合成联调

SQLite 模式与上文相同。MySQL 模式仅限隔离 MySQL 8.4 库 `advisor_phase6_lab`，要求专用账户及预置身份标记，只允许我们的合成快照；拒绝未知库，且不启用 Python RAG 或模型自动重试。Redis 在可选联调模式中缓存带版本和完整性校验的必需先修结果；数据库仍是任务和幂等事实源。缓存异常回读只读来源；Redis 限流不可用时，新任务提交返回 503，历史和 SSE 不依赖 Redis。

在 `ops/.env.example` 所列变量中填写四个**新生成的随机 64 位十六进制字符串**（例如 `openssl rand -hex 32`），保存在源码树外的私有环境文件。不要把已有数据库密码复制进来。从当前目录运行：

```sh
docker compose --env-file /path/to/private-lab.env -f ops/compose.yml up -d --build --wait
```

本机访问 `http://127.0.0.1:18095/`。Nginx 从 A/B 读取且不缓冲 SSE，未配置生产级故障转移；MySQL/Redis 默认不对宿主机开放。为原生联调可额外添加 `-f ops/compose.host.yml`，仅开放两个非默认回环测试端口。通过相同配置执行 `down` 停止，保留数据库卷；仅在确认丢弃合成库时执行 `down --volumes`。可选 `--profile metrics` 启动内部 Prometheus 抓取；`/metrics` 内容为实际计数，不包含学生或任务维度。

MySQL 队列支持两个实例竞争、数据库时钟租约、心跳、最多三次领取与旧者防写。可安全重复的读链在工作进程丢失后可能再次执行，数据库最多提交一次有效结果；不承诺外部请求 exactly once。Redis 不保管队列、任务结果或 SSE 事件。跨实例事件由数据库重放，设置连接上限与写截止，不是逐 Token 流。
