package guide

import (
	"context"
	"encoding/json"
	"strings"
	"time"
)

type Course struct {
	Prerequisites []string `json:"prerequisites"`
	Goal          string   `json:"goal"`
}

// Synthetic data only; no institutional curriculum is imported.
func Courses() map[string]Course {
	return map[string]Course{
		"编程入门": {[]string{}, "编写简单的程序"},
		"数据结构": {[]string{"编程入门"}, "选择和使用基本数据结构"},
		"算法实践": {[]string{"数据结构"}, "分析复杂度并实现基础算法"},
	}
}

type Model interface {
	Chat(context.Context, string) (string, error)
}
type LearningAgent struct {
	Model Model
	Tools ToolRegistry
}

func NewLearningAgent(model Model) *LearningAgent {
	return &LearningAgent{model, ToolRegistry{"course_lookup": func(ctx context.Context, params map[string]string) (string, error) {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		value, err := json.Marshal(Courses()[params["course"]])
		return string(value), err
	}}}
}
func (a *LearningAgent) Name() string                   { return "learning_guide" }
func (a *LearningAgent) ShouldHandle(query string) bool { return strings.TrimSpace(query) != "" }
func (a *LearningAgent) Run(ctx context.Context, query string) (Result, error) {
	// Fixed order avoids map iteration affecting queries naming multiple courses.
	for _, name := range []string{"编程入门", "数据结构", "算法实践"} {
		if strings.Contains(query, name) {
			raw, err := a.Tools.Execute(ctx, "course_lookup", map[string]string{"course": name}, time.Second)
			if err != nil {
				return Result{}, err
			}
			var info Course
			if err := json.Unmarshal([]byte(raw), &info); err != nil {
				return Result{}, err
			}
			prerequisites := strings.Join(info.Prerequisites, "、")
			if prerequisites == "" {
				prerequisites = "无先修要求"
			}
			return Result{"示例课程：" + name + "。先修：" + prerequisites + "。目标：" + info.Goal + "。", a.Name(), 0, []string{"course_lookup"}}, nil
		}
	}
	if a.Model != nil {
		answer, err := a.Model.Chat(ctx, query)
		return Result{answer, a.Name(), 0, []string{}}, err
	}
	return Result{"这是离线学习示例。可询问编程入门、数据结构或算法实践的先修关系。", a.Name(), 0, []string{}}, nil
}
