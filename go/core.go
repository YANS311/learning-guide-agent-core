package guide

import (
	"context"
	"errors"
	"fmt"
	"time"
)

type Result struct {
	FinalAnswer string   `json:"final_answer"`
	AgentName   string   `json:"agent_name"`
	TotalSteps  int      `json:"total_steps"`
	ToolsUsed   []string `json:"tools_used"`
}

type Agent interface {
	Name() string
	ShouldHandle(string) bool
	Run(context.Context, string) (Result, error)
}

type Dispatcher struct{ Agents []Agent }

func (d *Dispatcher) Dispatch(ctx context.Context, query string) (Result, error) {
	for _, agent := range d.Agents {
		if agent.ShouldHandle(query) {
			result, err := agent.Run(ctx, query)
			if err != nil {
				return Result{"处理您的问题时遇到了技术问题，请稍后重试。", agent.Name(), 0, []string{}}, err
			}
			result.AgentName = agent.Name()
			return result, nil
		}
	}
	return Result{"请补充学习目标和当前基础。", "none", 0, []string{}}, nil
}

// Tools must honor context cancellation. Retry only idempotent operations.
type Tool func(context.Context, map[string]string) (string, error)
type ToolRegistry map[string]Tool

func (r ToolRegistry) Execute(ctx context.Context, name string, params map[string]string, timeout time.Duration) (string, error) {
	tool, exists := r[name]
	if !exists {
		return "", fmt.Errorf("unknown tool: %s", name)
	}
	var last error
	for attempt := 0; attempt < 3; attempt++ {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		attemptCtx, cancel := context.WithTimeout(ctx, timeout)
		result, err := tool(attemptCtx, params)
		// A tool returning success after its deadline is still a failed call.
		if err == nil {
			err = attemptCtx.Err()
		}
		cancel()
		if err == nil {
			return result, nil
		}
		last = err
		if attempt < 2 {
			select {
			case <-ctx.Done():
				return "", ctx.Err()
			case <-time.After(time.Duration(attempt+1) * 100 * time.Millisecond):
			}
		}
	}
	return "", errors.New("tool failed after retries: " + last.Error())
}
