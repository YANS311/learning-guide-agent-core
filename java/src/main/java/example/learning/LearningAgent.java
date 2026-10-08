package example.learning;

import java.time.Duration;
import java.util.*;

public final class LearningAgent implements Core.Agent, AutoCloseable {
    public record Course(List<String> prerequisites, String goal) {}
    private final Core.Model model;
    private final Core.ToolRegistry tools = new Core.ToolRegistry();

    // Fictional examples, independent of any school or institutional curriculum.
    public static Map<String, Course> courses() {
        Map<String, Course> courses = new LinkedHashMap<>();
        courses.put("编程入门", new Course(List.of(), "编写简单的程序"));
        courses.put("数据结构", new Course(List.of("编程入门"), "选择和使用基本数据结构"));
        courses.put("算法实践", new Course(List.of("数据结构"), "分析复杂度并实现基础算法"));
        return Collections.unmodifiableMap(courses);
    }

    public LearningAgent(Core.Model model) {
        this.model = model;
        tools.register("course_lookup", parameters -> {
            Course course = courses().get(parameters.get("course"));
            if (course == null) throw new IllegalArgumentException("unknown course");
            String prerequisites = course.prerequisites().isEmpty() ? "无先修要求" : String.join("、", course.prerequisites());
            return "示例课程：" + parameters.get("course") + "。先修：" + prerequisites + "。目标：" + course.goal() + "。";
        });
    }

    @Override public String name() { return "learning_guide"; }
    @Override public boolean shouldHandle(String query) { return !query.isBlank(); }
    @Override public Core.Result run(String query) throws Exception {
        for (String course : courses().keySet()) {
            if (query.contains(course)) {
                String answer = tools.execute("course_lookup", Map.of("course", course), Duration.ofSeconds(1));
                return new Core.Result(answer, name(), 0, List.of("course_lookup"));
            }
        }
        String answer = model == null
            ? "这是离线学习示例。可询问编程入门、数据结构或算法实践的先修关系。"
            : model.chat(query);
        return new Core.Result(answer, name(), 0, List.of());
    }
    @Override public void close() { tools.close(); }
}
