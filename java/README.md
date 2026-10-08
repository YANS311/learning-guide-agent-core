# Java 脱敏导学示例

独立实现的通用学习助手，使用三个虚构课程，支持课程查询、可选模型问答、工具重试与超时、SSE 和累计统计。完整功能差异与脱敏范围见 [根 README](../README.md)。

需要 JDK 17+、Maven 3.9+：

```bash
mvn clean verify
java -jar target/learning-guide-java-0.1.0.jar
```

默认服务为 `http://127.0.0.1:8081`。接口包括 `/api/health`、`/api/courses`、`/api/chat`、`/api/chat/stream`、`/api/metrics`，问答接口使用 POST，其余使用 GET。

默认无需模型服务。可通过环境变量 `PORT`、`LLM_ENABLED`、`LLM_BASE_URL`、`LLM_API_KEY`、`LLM_MODEL` 自行配置；不会读取 `.env`。`src/test/` 使用 JUnit 验证 HTTP、输入、SSE、失败兜底和本机模拟模型请求。
