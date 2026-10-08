package guide

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

type LLMClient struct {
	BaseURL, APIKey, Model string
	HTTP                   *http.Client
}

func (c *LLMClient) Chat(ctx context.Context, query string) (string, error) {
	payload, err := json.Marshal(map[string]any{
		"model": c.Model, "max_completion_tokens": 1500, "temperature": 0.3,
		"messages": []map[string]string{
			{"role": "system", "content": "你是通用学习助手。用简明步骤解释学习方法，不编造课程政策。"},
			{"role": "user", "content": query},
		},
	})
	if err != nil {
		return "", err
	}
	client := c.HTTP
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	var last error
	for attempt := 0; attempt < 2; attempt++ {
		request, err := http.NewRequestWithContext(ctx, "POST", strings.TrimRight(c.BaseURL, "/")+"/chat/completions", bytes.NewReader(payload))
		if err != nil {
			return "", errors.New("invalid model configuration")
		}
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("Authorization", "Bearer "+c.APIKey)
		response, err := client.Do(request)
		if err == nil {
			body, readErr := io.ReadAll(io.LimitReader(response.Body, 1024*1024+1))
			response.Body.Close()
			if response.StatusCode >= 200 && response.StatusCode < 300 {
				if readErr != nil || len(body) > 1024*1024 {
					return "", errors.New("invalid model response")
				}
				var parsed struct {
					Choices []struct {
						Message struct {
							Content string `json:"content"`
						} `json:"message"`
					} `json:"choices"`
				}
				if json.Unmarshal(body, &parsed) != nil || len(parsed.Choices) == 0 || strings.TrimSpace(parsed.Choices[0].Message.Content) == "" {
					return "", errors.New("invalid model response")
				}
				return strings.TrimSpace(parsed.Choices[0].Message.Content), nil
			}
			last = fmt.Errorf("model HTTP status %d", response.StatusCode)
			if response.StatusCode < 500 && response.StatusCode != 429 {
				return "", last
			}
		} else {
			last = errors.New("model request failed")
		}
		if attempt == 0 {
			select {
			case <-ctx.Done():
				return "", ctx.Err()
			case <-time.After(time.Second):
			}
		}
	}
	return "", last
}
