package example.learning;

import com.fasterxml.jackson.databind.*;
import com.sun.net.httpserver.*;
import java.io.*;
import java.net.InetSocketAddress;
import java.nio.charset.StandardCharsets;
import java.util.*;
import java.util.concurrent.*;

/** Independent loopback-only HTTP example; no files or user messages are retained. */
public final class App implements AutoCloseable {
    private final ObjectMapper json = new ObjectMapper().enable(DeserializationFeature.FAIL_ON_TRAILING_TOKENS);
    private final HttpServer server;
    private final ExecutorService executor = Executors.newFixedThreadPool(8);
    private final LearningAgent agent;
    private final Core.Dispatcher dispatcher = new Core.Dispatcher();
    private final boolean modelEnabled;
    private final Map<String, Integer> tools = new TreeMap<>(), routes = new TreeMap<>();
    private int requests, failures;
    private double totalLatency;

    public App(int port, Core.Model model) throws IOException {
        modelEnabled = model != null;
        agent = new LearningAgent(model);
        dispatcher.register(agent);
        server = HttpServer.create(new InetSocketAddress("127.0.0.1", port), 32);
        server.setExecutor(executor);
        server.createContext("/", this::handle);
    }
    public void start() { server.start(); }
    public int port() { return server.getAddress().getPort(); }
    private String mode() { return modelEnabled ? "model" : "offline_demo"; }

    private synchronized void record(Core.Result result, double milliseconds, boolean failed) {
        requests++;
        totalLatency += milliseconds;
        if (failed) failures++;
        routes.merge(modelEnabled && result.tools_used().isEmpty() ? "model" : "demo", 1, Integer::sum);
        for (String tool : result.tools_used()) tools.merge(tool, 1, Integer::sum);
    }
    private synchronized Map<String, Object> metrics() {
        return Map.of("total_requests", requests, "error_count", failures,
            "avg_latency_ms", requests == 0 ? 0 : totalLatency / requests,
            "tool_usage_counts", new TreeMap<>(tools), "by_route", new TreeMap<>(routes));
    }

    private void sendJSON(HttpExchange exchange, int status, Object value) throws IOException {
        byte[] bytes = json.writeValueAsBytes(value);
        exchange.getResponseHeaders().set("Content-Type", "application/json; charset=utf-8");
        exchange.sendResponseHeaders(status, bytes.length);
        exchange.getResponseBody().write(bytes);
    }

    private void handle(HttpExchange exchange) throws IOException {
        try (exchange) {
            String path = exchange.getRequestURI().getPath();
            boolean chat = path.equals("/api/chat") || path.equals("/api/chat/stream");
            if (!Set.of("/", "/api/health", "/api/courses", "/api/metrics", "/api/chat", "/api/chat/stream").contains(path)) {
                sendJSON(exchange, 404, Map.of("error", "not found")); return;
            }
            String method = chat ? "POST" : "GET";
            if (!exchange.getRequestMethod().equals(method)) {
                exchange.getResponseHeaders().set("Allow", method);
                sendJSON(exchange, 405, Map.of("error", "method not allowed")); return;
            }
            if (!chat) {
                Object value = switch (path) {
                    case "/" -> Map.of("name", "通用智能导学 Java 示例", "mode", mode());
                    case "/api/health" -> Map.of("status", "ok", "mode", mode());
                    case "/api/courses" -> Map.of("synthetic", true, "courses", LearningAgent.courses());
                    default -> metrics();
                };
                sendJSON(exchange, 200, value); return;
            }
            String query;
            try {
                byte[] body = exchange.getRequestBody().readNBytes(16385);
                if (body.length > 16384) throw new IllegalArgumentException();
                JsonNode parsed = json.readTree(body);
                JsonNode message = parsed == null ? null : parsed.get("message");
                if (message == null || !message.isTextual()) throw new IllegalArgumentException();
                query = message.asText();
                if (query.isBlank() || query.codePointCount(0, query.length()) > 2000) throw new IllegalArgumentException();
            } catch (IOException | IllegalArgumentException invalid) {
                sendJSON(exchange, 422, Map.of("error", "message must contain 1–2000 characters")); return;
            }
            boolean stream = path.equals("/api/chat/stream");
            if (stream) {
                exchange.getResponseHeaders().set("Content-Type", "text/event-stream; charset=utf-8");
                exchange.getResponseHeaders().set("Cache-Control", "no-cache");
                exchange.sendResponseHeaders(200, 0);
                event(exchange, "status", Map.of("status", "processing"));
            }
            long start = System.nanoTime();
            Core.Result result;
            boolean failed = false;
            try { result = dispatcher.dispatch(query); }
            catch (Exception error) {
                failed = true;
                if (error instanceof InterruptedException) Thread.currentThread().interrupt();
                result = new Core.Result("处理您的问题时遇到了技术问题，请稍后重试。", agent.name(), 0, List.of());
            }
            record(result, (System.nanoTime() - start) / 1_000_000.0, failed);
            if (stream) {
                event(exchange, "answer", result);
                event(exchange, "done", Map.of());
            } else sendJSON(exchange, 200, result);
        }
    }

    private void event(HttpExchange exchange, String name, Object value) throws IOException {
        String event = "event: " + name + "\ndata: " + json.writeValueAsString(value) + "\n\n";
        exchange.getResponseBody().write(event.getBytes(StandardCharsets.UTF_8));
        exchange.getResponseBody().flush();
    }
    @Override public void close() { server.stop(0); executor.shutdownNow(); agent.close(); }
    private static String env(String name, String fallback) { return System.getenv().getOrDefault(name, fallback); }
    public static void main(String[] args) throws Exception {
        int port = Integer.parseInt(env("PORT", "8081"));
        if (port < 1 || port > 65535) throw new IllegalArgumentException("PORT must be between 1 and 65535");
        Core.Model model = Boolean.parseBoolean(env("LLM_ENABLED", "false"))
            ? new LLMClient(env("LLM_BASE_URL", "http://localhost:11434/v1"), env("LLM_API_KEY", "ollama"), env("LLM_MODEL", "qwen2.5:7b")) : null;
        App app = new App(port, model);
        Runtime.getRuntime().addShutdownHook(new Thread(app::close));
        app.start();
        System.out.println("Learning guide Java example: http://127.0.0.1:" + port);
    }
}
