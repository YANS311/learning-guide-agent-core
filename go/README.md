# Go 脱敏导学示例

独立实现的通用学习助手，使用三个虚构课程，支持课程查询、可选模型问答、工具重试与 context 取消、SSE 和累计统计。完整功能差异与脱敏范围见 [根 README](../README.md)。

需要 Go 1.22+，只依赖标准库：

```bash
go test ./...
go vet ./...
go run ./cmd/server
```

默认服务为 `http://127.0.0.1:8082`。接口包括 `/api/health`、`/api/courses`、`/api/chat`、`/api/chat/stream`、`/api/metrics`，问答接口使用 POST，其余使用 GET。

默认无需模型服务。可通过环境变量 `PORT`、`LLM_ENABLED`、`LLM_BASE_URL`、`LLM_API_KEY`、`LLM_MODEL` 自行配置；不会读取 `.env`。`server_test.go` 验证 HTTP、输入、SSE、错误兜底、工具取消、本机模拟模型与并发统计。
