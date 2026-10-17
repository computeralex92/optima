package explain

import "testing"

func TestResolveGeneric(t *testing.T) {
	c := Resolve("https://api.deepseek.com/v1", "key", "deepseek-chat", "https://azure", "azkey", "deploy")
	if c != (Config{BaseURL: "https://api.deepseek.com/v1", APIKey: "key", Model: "deepseek-chat"}) {
		t.Fatalf("config = %+v", c)
	}
	if !c.Configured() {
		t.Fatal("generic config should be configured")
	}
	if got := c.chatURL(); got != "https://api.deepseek.com/v1/chat/completions" {
		t.Fatalf("chatURL = %q", got)
	}
}

func TestResolveGenericMissingModelDoesNotFallBack(t *testing.T) {
	c := Resolve("https://api.deepseek.com/v1", "", "", "https://azure", "azkey", "deploy")
	if c.Azure {
		t.Fatalf("must not fall back to Azure: %+v", c)
	}
	if c.Configured() {
		t.Fatalf("missing model must be unconfigured: %+v", c)
	}
}

func TestResolveAzureFallback(t *testing.T) {
	c := Resolve("", "", "", "https://example.openai.azure.com/", "azkey", "deploy")
	if !c.Azure || c.Model != "deploy" || c.APIKey != "azkey" {
		t.Fatalf("config = %+v", c)
	}
	if !c.Configured() {
		t.Fatal("Azure config should be configured")
	}
	if got := c.chatURL(); got != "https://example.openai.azure.com/openai/v1/chat/completions" {
		t.Fatalf("chatURL = %q", got)
	}
}

func TestResolveAzureDefaultDeployment(t *testing.T) {
	if got := Resolve("", "", "", "https://azure", "azkey", "").Model; got != "gpt-6-luna" {
		t.Fatalf("model = %q", got)
	}
}

func TestChatURLVariants(t *testing.T) {
	cases := []struct {
		cfg  Config
		want string
	}{
		{Config{BaseURL: "https://api.openai.com/v1/"}, "https://api.openai.com/v1/chat/completions"},
		{Config{BaseURL: "https://api.openai.com/v1/chat/completions"}, "https://api.openai.com/v1/chat/completions"},
		{Config{BaseURL: "http://localhost:11434/v1", Model: "x"}, "http://localhost:11434/v1/chat/completions"},
		{Config{BaseURL: "https://azure/", Azure: true}, "https://azure/openai/v1/chat/completions"},
	}
	for _, tc := range cases {
		if got := tc.cfg.chatURL(); got != tc.want {
			t.Errorf("chatURL(%+v) = %q, want %q", tc.cfg, got, tc.want)
		}
	}
}

func TestConfiguredRequiresAzureKey(t *testing.T) {
	if (Config{BaseURL: "https://azure", Model: "deploy", Azure: true}).Configured() {
		t.Fatal("Azure without a key must be unconfigured")
	}
}
