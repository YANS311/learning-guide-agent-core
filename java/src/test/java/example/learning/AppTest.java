package example.learning;

import com.fasterxml.jackson.databind.*;
import com.sun.net.httpserver.HttpServer;
import org.junit.jupiter.api.Test;
import java.io.IOException;
import java.net.*;
import java.net.http.*;
import java.nio.charset.StandardCharsets;
import java.time.Duration;
import java.util.*;
import java.util.concurrent.atomic.AtomicInteger;
import static org.junit.jupiter.api.Assertions.*;

class AppTest {
    private final ObjectMapper json = new ObjectMapper();
    private final HttpClient http = HttpClient.newHttpClient();
    private HttpResponse<String> request(App app, String method, String path, String body) throws Exception {
        return http.send(HttpRequest.newBuilder(URI.create("http://127.0.0.1:" + app.port() + path))
            .timeout(Duration.ofSeconds(5)).method(method, body == null ? HttpRequest.BodyPublishers.noBody() : HttpRequest.BodyPublishers.ofString(body))
            .build(), HttpResponse.BodyHandlers.ofString(StandardCharsets.UTF_8));
    }

    @Test void offlineCoursesAndPrivateMetrics() throws Exception {
        try (App app = new App(0, null)) {
            app.start();
            assertEquals("offline_demo", json.readTree(request(app, "GET", "/api/health", null).body()).get("mode").asText());
            var answer = request(app, "POST", "/api/chat", "{\"message\":\"数据结构先修\"}");
            assertEquals(200, answer.statusCode());
            assertTrue(answer.body().contains("编程入门"));
            JsonNode metrics = json.readTree(request(app, "GET", "/api/metrics", null).body());
            assertEquals(1, metrics.get("total_requests").asInt());
            assertEquals(1, metrics.get("tool_usage_counts").get("course_lookup").asInt());
            assertFalse(metrics.has("intent_accuracy"));
            assertFalse(metrics.toString().contains("数据结构"));
            assertTrue(json.readTree(request(app, "GET", "/api/courses", null).body()).get("synthetic").asBoolean());
        }
    }

    @Test void validatesJSONUnicodeAndMethods() throws Exception {
        try (App app = new App(0, null)) {
            app.start();
            for (String body : List.of("{}", "[]", "{\"message\":null}", "{\"message\":3}", "{\"message\":\" \"}", "{\"message\":\"\"}", "{\"message\":\"x\"} {}", "{\"message\":\"" + "界".repeat(2001) + "\"}")) {
                assertEquals(422, request(app, "POST", "/api/chat", body).statusCode());
            }
            assertEquals(200, request(app, "POST", "/api/chat", "{\"message\":\"" + "界".repeat(2000) + "\"}").statusCode());
            assertEquals(405, request(app, "GET", "/api/chat", null).statusCode());
            assertEquals(404, request(app, "GET", "/unknown", null).statusCode());
        }
    }

    @Test void SSEEventsAndOfflineFallback() throws Exception {
        try (App app = new App(0, null)) {
            app.start();
            var response = request(app, "POST", "/api/chat/stream", "{\"message\":\"算法实践先修\"}");
            assertTrue(response.headers().firstValue("Content-Type").orElse("").startsWith("text/event-stream"));
            assertTrue(response.body().contains("数据结构"));
            assertTrue(response.body().indexOf("event: status") < response.body().indexOf("event: answer"));
            assertTrue(response.body().indexOf("event: answer") < response.body().indexOf("event: done"));
            assertTrue(request(app, "POST", "/api/chat", "{\"message\":\"如何复习\"}").body().contains("离线学习示例"));
        }
    }

    @Test void modelFailureDoesNotExposeErrorAndIsCounted() throws Exception {
        try (App app = new App(0, query -> { throw new IOException("private provider error"); })) {
            app.start();
            String body = request(app, "POST", "/api/chat", "{\"message\":\"如何复习\"}").body();
            assertTrue(body.contains("技术问题"));
            assertFalse(body.contains("private"));
            assertEquals(1, json.readTree(request(app, "GET", "/api/metrics", null).body()).get("error_count").asInt());
        }
    }

    @Test void toolRetryAndTimeout() throws Exception {
        try (var tools = new Core.ToolRegistry()) {
            AtomicInteger calls = new AtomicInteger();
            tools.register("read", params -> {
                if (calls.incrementAndGet() == 1) throw new IOException("temporary");
                return "recovered";
            });
            assertEquals("recovered", tools.execute("read", Map.of(), Duration.ofSeconds(1)));
            assertEquals(2, calls.get());
            tools.register("slow", params -> { Thread.sleep(5000); return "late"; });
            assertThrows(Exception.class, () -> tools.execute("slow", Map.of(), Duration.ofMillis(10)));
        }
    }

    @Test void modelTransportRetriesTransientErrorsButNot401() throws Exception {
        for (int status : List.of(401, 500)) {
            AtomicInteger calls = new AtomicInteger();
            HttpServer provider = HttpServer.create(new InetSocketAddress("127.0.0.1", 0), 0);
            provider.createContext("/v1/chat/completions", exchange -> {
                try (exchange) {
                    exchange.getRequestBody().readAllBytes();
                    if (calls.incrementAndGet() == 1) { exchange.sendResponseHeaders(status, -1); return; }
                    byte[] bytes = "{\"choices\":[{\"message\":{\"content\":\"示例回答\"}}]}".getBytes(StandardCharsets.UTF_8);
                    exchange.sendResponseHeaders(200, bytes.length);
                    exchange.getResponseBody().write(bytes);
                }
            });
            provider.start();
            try {
                var model = new LLMClient("http://127.0.0.1:" + provider.getAddress().getPort() + "/v1", "test-only-placeholder", "demo");
                if (status == 401) { assertThrows(IOException.class, () -> model.chat("如何复习")); assertEquals(1, calls.get()); }
                else { assertEquals("示例回答", model.chat("如何复习")); assertEquals(2, calls.get()); }
            } finally { provider.stop(0); }
        }
    }
}
