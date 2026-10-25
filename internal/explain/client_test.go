package explain

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestCompleteGenericRequest(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" {
			t.Errorf("path = %q", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer test-key" {
			t.Errorf("Authorization = %q", got)
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if body["model"] != "deepseek-chat" {
			t.Errorf("model = %v", body["model"])
		}
		if body["max_tokens"] != float64(1500) {
			t.Errorf("max_tokens = %v", body["max_tokens"])
		}
		if _, ok := body["prompt_cache_retention"]; ok {
			t.Error("generic body must not carry an Azure-only cache field")
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"choices":[{"message":{"content":"Sunny \u2014 cheap"}}],"usage":{"total_tokens":42}}`)
	}))
	defer srv.Close()

	c := NewClient(Config{BaseURL: srv.URL, APIKey: "test-key", Model: "deepseek-chat"})
	text, usage, err := c.Complete(context.Background(), "system", "user")
	if err != nil {
		t.Fatal(err)
	}
	if text != "Sunny, cheap" {
		t.Fatalf("text = %q", text)
	}
	if usage.TotalTokens != 42 {
		t.Fatalf("tokens = %d", usage.TotalTokens)
	}
}

func TestCompleteGenericWithoutKey(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "" {
			t.Errorf("Authorization = %q, want empty", got)
		}
		io.WriteString(w, `{"choices":[{"message":{"content":"ok"}}]}`)
	}))
	defer srv.Close()

	c := NewClient(Config{BaseURL: srv.URL, Model: "local-model"})
	if _, _, err := c.Complete(context.Background(), "s", "u"); err != nil {
		t.Fatal(err)
	}
}

func TestCompleteGenericFullURL(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			t.Errorf("path = %q", r.URL.Path)
		}
		io.WriteString(w, `{"choices":[{"message":{"content":"ok"}}]}`)
	}))
	defer srv.Close()

	c := NewClient(Config{BaseURL: srv.URL + "/v1/chat/completions", Model: "m"})
	if _, _, err := c.Complete(context.Background(), "s", "u"); err != nil {
		t.Fatal(err)
	}
}

func TestCompleteAzureRequest(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/openai/v1/chat/completions" {
			t.Errorf("path = %q", r.URL.Path)
		}
		if got := r.Header.Get("api-key"); got != "azkey" {
			t.Errorf("api-key = %q", got)
		}
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		if body["max_completion_tokens"] != float64(1500) {
			t.Errorf("max_completion_tokens = %v", body["max_completion_tokens"])
		}
		if body["reasoning_effort"] != "low" {
			t.Errorf("reasoning_effort = %v", body["reasoning_effort"])
		}
		if body["prompt_cache_retention"] != "24h" {
			t.Errorf("prompt_cache_retention = %v", body["prompt_cache_retention"])
		}
		if _, ok := body["max_tokens"]; ok {
			t.Error("Azure body must not carry max_tokens")
		}
		io.WriteString(w, `{"choices":[{"message":{"content":"ok"}}]}`)
	}))
	defer srv.Close()

	c := NewClient(Config{BaseURL: srv.URL, APIKey: "azkey", Model: "deploy", Azure: true})
	if _, _, err := c.Complete(context.Background(), "s", "u"); err != nil {
		t.Fatal(err)
	}
}

func TestCompleteProviderError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		io.WriteString(w, "slow down")
	}))
	defer srv.Close()

	c := NewClient(Config{BaseURL: srv.URL, Model: "m"})
	_, _, err := c.Complete(context.Background(), "s", "u")
	if err == nil || !strings.Contains(err.Error(), "model returned 429: slow down") {
		t.Fatalf("err = %v", err)
	}
}

func TestCompleteEmptyAnswer(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"choices":[]}`)
	}))
	defer srv.Close()

	c := NewClient(Config{BaseURL: srv.URL, Model: "m"})
	_, _, err := c.Complete(context.Background(), "s", "u")
	if err == nil || err.Error() != "model returned no answer" {
		t.Fatalf("err = %v", err)
	}
}

func TestCompleteEmptyContent(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"choices":[{"message":{"content":"  "}}]}`)
	}))
	defer srv.Close()

	c := NewClient(Config{BaseURL: srv.URL, Model: "m"})
	_, _, err := c.Complete(context.Background(), "s", "u")
	if err == nil || err.Error() != "model returned no answer" {
		t.Fatalf("err = %v", err)
	}
}

func TestCompleteTransportError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	url := srv.URL
	srv.Close()

	c := NewClient(Config{BaseURL: url, Model: "m"})
	_, _, err := c.Complete(context.Background(), "s", "u")
	if err == nil || !strings.Contains(err.Error(), "model unreachable") {
		t.Fatalf("err = %v", err)
	}
}

func TestCompleteTimeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(100 * time.Millisecond)
		io.WriteString(w, `{"choices":[{"message":{"content":"late"}}]}`)
	}))
	defer srv.Close()

	c := NewClient(Config{BaseURL: srv.URL, Model: "m"})
	c.httpClient = &http.Client{Timeout: 10 * time.Millisecond}
	_, _, err := c.Complete(context.Background(), "s", "u")
	if err == nil || !strings.Contains(err.Error(), "model unreachable") {
		t.Fatalf("err = %v", err)
	}
}
