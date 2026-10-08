package example.learning;

import java.time.Duration;
import java.util.*;
import java.util.concurrent.*;

/** Reusable agent contracts and idempotent tool execution. */
public final class Core {
    private Core() {}

    public record Result(String final_answer, String agent_name, int total_steps, List<String> tools_used) {}
    public interface Agent {
        String name();
        boolean shouldHandle(String query);
        Result run(String query) throws Exception;
    }
    public interface Model { String chat(String query) throws Exception; }
    public interface Tool { String call(Map<String, String> parameters) throws Exception; }

    public static final class ToolRegistry implements AutoCloseable {
        private final Map<String, Tool> tools = new HashMap<>();
        private final ExecutorService executor = Executors.newFixedThreadPool(2);

        public void register(String name, Tool tool) { tools.put(name, tool); }

        public String execute(String name, Map<String, String> parameters, Duration timeout) throws Exception {
            Tool tool = tools.get(name);
            if (tool == null) throw new IllegalArgumentException("unknown tool");
            Exception last = null;
            for (int attempt = 0; attempt < 3; attempt++) {
                Future<String> task = executor.submit(() -> tool.call(parameters));
                try {
                    return task.get(timeout.toMillis(), TimeUnit.MILLISECONDS);
                } catch (InterruptedException error) {
                    task.cancel(true);
                    Thread.currentThread().interrupt();
                    throw error;
                } catch (ExecutionException | TimeoutException error) {
                    task.cancel(true);
                    last = error;
                }
                if (attempt < 2) Thread.sleep((attempt + 1) * 100L);
            }
            throw new Exception("tool failed after retries", last);
        }

        @Override public void close() { executor.shutdownNow(); }
    }

    public static final class Dispatcher {
        private final List<Agent> agents = new ArrayList<>();
        public void register(Agent agent) { agents.add(agent); }
        public Result dispatch(String query) throws Exception {
            for (Agent agent : agents) {
                boolean matches;
                try { matches = agent.shouldHandle(query); }
                catch (RuntimeException error) { continue; }
                if (matches) return agent.run(query);
            }
            return new Result("请补充学习目标和当前基础。", "none", 0, List.of());
        }
    }
}
