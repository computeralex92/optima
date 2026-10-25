package explain

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

// Client makes one chat-completions request against the configured provider.
type Client struct {
	cfg        Config
	httpClient *http.Client
}

// NewClient builds a client with the upstream 45 second timeout.
func NewClient(cfg Config) *Client {
	return &Client{cfg: cfg, httpClient: &http.Client{Timeout: 45 * time.Second}}
}

// Configured reports whether the client has enough configuration to call a
// provider. The API checks this before building a digest.
func (c *Client) Configured() bool {
	return c.cfg.Configured()
}

// Usage is the provider token usage, when present.
type Usage struct {
	TotalTokens int `json:"total_tokens"`
}

type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type chatRequest struct {
	Model               string        `json:"model"`
	Messages            []chatMessage `json:"messages"`
	MaxTokens           int           `json:"max_tokens,omitempty"`
	MaxCompletionTokens int           `json:"max_completion_tokens,omitempty"`
	ReasoningEffort     string        `json:"reasoning_effort,omitempty"`
	// System prompt is identical across requests: keep its cache warm for a day.
	PromptCacheRetention string `json:"prompt_cache_retention,omitempty"`
}

type chatResponse struct {
	Choices []struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
	} `json:"choices"`
	Usage Usage `json:"usage"`
}

// Complete sends the system and user messages and returns the cleaned answer.
// The generic dialect uses max_tokens and Bearer auth; Azure uses the
// max_completion_tokens/reasoning_effort shape and the api-key header.
func (c *Client) Complete(ctx context.Context, system, user string) (string, Usage, error) {
	if !c.cfg.Configured() {
		return "", Usage{}, errors.New("explain is not configured")
	}

	body := chatRequest{
		Model: c.cfg.Model,
		Messages: []chatMessage{
			{Role: "system", Content: system},
			{Role: "user", Content: user},
		},
	}
	if c.cfg.Azure {
		body.MaxCompletionTokens = 1500
		body.ReasoningEffort = "low"
		body.PromptCacheRetention = "24h"
	} else {
		body.MaxTokens = 1500
	}

	raw, err := json.Marshal(body)
	if err != nil {
		return "", Usage{}, fmt.Errorf("model unreachable: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.cfg.chatURL(), bytes.NewReader(raw))
	if err != nil {
		return "", Usage{}, fmt.Errorf("model unreachable: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if c.cfg.Azure {
		req.Header.Set("api-key", c.cfg.APIKey)
	} else if c.cfg.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.cfg.APIKey)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", Usage{}, fmt.Errorf("model unreachable: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		detail, _ := io.ReadAll(io.LimitReader(resp.Body, 200))
		return "", Usage{}, fmt.Errorf("model returned %d: %s", resp.StatusCode, strings.TrimSpace(string(detail)))
	}

	var out chatResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", Usage{}, errors.New("model returned no answer")
	}
	if len(out.Choices) == 0 {
		return "", Usage{}, errors.New("model returned no answer")
	}
	text := strings.TrimSpace(out.Choices[0].Message.Content)
	if text == "" {
		return "", Usage{}, errors.New("model returned no answer")
	}

	text = strings.ReplaceAll(text, " \u2014 ", ", ")
	text = strings.ReplaceAll(text, "\u2014", ", ")
	text = strings.ReplaceAll(text, "\u2013", "-")
	return text, out.Usage, nil
}
