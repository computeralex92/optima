package explain

import "strings"

// Config is the resolved provider configuration for plan explanations.
// Azure selects the Azure request shape and the api-key header.
type Config struct {
	BaseURL string
	APIKey  string
	Model   string
	Azure   bool
}

// Resolve selects the generic OpenAI-compatible provider when a base URL is
// set, otherwise the upstream Azure fallback. A generic base URL without a
// model stays unconfigured; it never silently falls back to Azure.
func Resolve(genericBaseURL, genericAPIKey, genericModel, azureEndpoint, azureAPIKey, azureDeployment string) Config {
	if genericBaseURL != "" {
		return Config{BaseURL: genericBaseURL, APIKey: genericAPIKey, Model: genericModel}
	}
	if azureEndpoint != "" && azureAPIKey != "" {
		if azureDeployment == "" {
			azureDeployment = "gpt-6-luna"
		}
		return Config{BaseURL: azureEndpoint, APIKey: azureAPIKey, Model: azureDeployment, Azure: true}
	}
	return Config{}
}

// Configured reports whether a provider call can be made. A generic provider
// needs a base URL and model; a key is optional for local endpoints. Azure
// also needs a key.
func (c Config) Configured() bool {
	if c.BaseURL == "" || c.Model == "" {
		return false
	}
	return !c.Azure || c.APIKey != ""
}

// chatURL returns the chat-completions URL for the selected dialect.
func (c Config) chatURL() string {
	base := strings.TrimSuffix(c.BaseURL, "/")
	if c.Azure {
		return base + "/openai/v1/chat/completions"
	}
	if strings.HasSuffix(base, "/chat/completions") {
		return base
	}
	return base + "/chat/completions"
}
