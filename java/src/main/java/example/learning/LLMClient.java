package example.learning;

import com.fasterxml.jackson.databind.JsonNode;
import com.fasterxml.jackson.databind.ObjectMapper;
import java.io.IOException;
import java.net.URI;
import java.net.http.*;
import java.time.Duration;
import java.util.*;

/** Optional OpenAI-compatible chat transport. Does not log prompts or credentials. */
public final class LLMClient implements Core.Model {
    private final URI endpoint;
    private final String apiKey, model;
    private final ObjectMapper json = new ObjectMapper();
    private final HttpClient client = HttpClient.newBuilder().connectTimeout(Duration.ofSeconds(3)).build();

    public LLMClient(String baseURL, String apiKey, String model) {
        this.endpoint = URI.create(baseURL.replaceAll("/+$", "") + "/chat/completions");
        this.apiKey = apiKey;
        this.model = model;
    }

    @Override public String chat(String query) throws Exception {
        String payload = json.writeValueAsString(Map.of(
            "model", model, "max_completion_tokens", 1500, "temperature", 0.3,
            "messages", List.of(
                Map.of("role", "system", "content", "你是通用学习助手。用简明步骤解释学习方法，不编造课程政策。"),
                Map.of("role", "user", "content", query))));
        for (int attempt = 0; attempt < 2; attempt++) {
            HttpResponse<byte[]> response;
            try {
                HttpRequest request = HttpRequest.newBuilder(endpoint).timeout(Duration.ofSeconds(10))
                    .header("Content-Type", "application/json").header("Authorization", "Bearer " + apiKey)
                    .POST(HttpRequest.BodyPublishers.ofString(payload)).build();
                response = client.send(request, HttpResponse.BodyHandlers.ofByteArray());
            } catch (IOException error) {
                if (attempt == 1) throw new IOException("model request failed");
                Thread.sleep(1000);
                continue;
            }
            if (response.statusCode() >= 200 && response.statusCode() < 300) {
                byte[] bytes = response.body();
                if (bytes.length > 1024 * 1024) throw new IOException("invalid model response");
                JsonNode parsed = json.readTree(bytes);
                JsonNode content = parsed == null ? null : parsed.path("choices").path(0).path("message").path("content");
                if (content == null || !content.isTextual() || content.asText().isBlank()) throw new IOException("invalid model response");
                return content.asText().strip();
            }
            if (response.statusCode() < 500 && response.statusCode() != 429)
                throw new IOException("model HTTP status " + response.statusCode());
            if (attempt == 0) Thread.sleep(1000);
        }
        throw new IOException("model unavailable after retries");
    }
}
