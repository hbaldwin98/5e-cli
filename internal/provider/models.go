package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"
)

// defaultOpenAIBaseURL mirrors ask.DefaultBaseURL. It's duplicated (instead
// of imported) because ask imports provider for credential resolution;
// provider importing ask back would cycle.
const defaultOpenAIBaseURL = "https://api.openai.com/v1"

// FetchModels calls the provider's OpenAI-compatible GET /models endpoint
// (OpenAI and OpenRouter both implement this) and returns the model ids,
// sorted. client may be nil, in which case a client with a short timeout is
// used so a hung endpoint doesn't block the CLI indefinitely.
func FetchModels(ctx context.Context, client *http.Client, name string, cred Credential) ([]string, error) {
	if cred.APIKey == "" {
		return nil, fmt.Errorf("%s has no API key configured; run `5e auth login %s`", name, name)
	}
	base := cred.BaseURL
	if base == "" {
		base = DefaultBaseURL(name)
	}
	if base == "" {
		base = defaultOpenAIBaseURL
	}
	base = strings.TrimRight(base, "/")

	if client == nil {
		client = &http.Client{Timeout: 20 * time.Second}
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/models", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+cred.APIKey)

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("list models: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("list models: read response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("list models: %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}

	var parsed struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, fmt.Errorf("list models: parse response: %w", err)
	}

	ids := make([]string, 0, len(parsed.Data))
	for _, m := range parsed.Data {
		if m.ID != "" {
			ids = append(ids, m.ID)
		}
	}
	sort.Strings(ids)
	return ids, nil
}
