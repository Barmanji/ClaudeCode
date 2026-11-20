package config

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// ErrInvalidKey means the provider definitively rejected the credentials.
var ErrInvalidKey = errors.New("the provider rejected this API key")

// ErrModelsUnavailable means the endpoint responded but does not expose a
// model catalogue. Credentials may still be fine.
var ErrModelsUnavailable = errors.New("provider does not expose a model list")

type httpError struct {
	status int
	body   string
}

func (e *httpError) Error() string {
	body := strings.TrimSpace(e.body)
	if len(body) > 300 {
		body = body[:300] + "…"
	}
	if body == "" {
		return fmt.Sprintf("provider returned HTTP %d", e.status)
	}
	return fmt.Sprintf("provider returned HTTP %d: %s", e.status, body)
}

func newHTTPClient() *http.Client {
	return &http.Client{Timeout: 10 * time.Second}
}

func doRequest(ctx context.Context, baseURL, key, path string) ([]byte, error) {
	url := strings.TrimRight(baseURL, "/") + path
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	req.Header.Set("Accept", "application/json")
	// OpenRouter uses these for attribution.
	if strings.Contains(baseURL, "openrouter.ai") {
		req.Header.Set("HTTP-Referer", "https://github.com/Barmanji/ClaudeCode")
		req.Header.Set("X-Title", AppName)
	}

	resp, err := newHTTPClient().Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, err
	}
	switch {
	case resp.StatusCode == http.StatusOK:
		return body, nil
	case resp.StatusCode == http.StatusUnauthorized, resp.StatusCode == http.StatusForbidden:
		return nil, fmt.Errorf("%w: %v", ErrInvalidKey, &httpError{status: resp.StatusCode, body: string(body)})
	case resp.StatusCode == http.StatusNotFound:
		return nil, ErrModelsUnavailable
	default:
		return nil, &httpError{status: resp.StatusCode, body: string(body)}
	}
}

type modelListResponse struct {
	Data []struct {
		ID string `json:"id"`
	} `json:"data"`
}

// FetchModels lists model IDs from an OpenAI-compatible /models endpoint.
func FetchModels(ctx context.Context, baseURL, key string) ([]string, error) {
	body, err := doRequest(ctx, baseURL, key, "/models")
	if err != nil {
		return nil, err
	}
	var payload modelListResponse
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, fmt.Errorf("parse model list: %w", err)
	}
	ids := make([]string, 0, len(payload.Data))
	for _, m := range payload.Data {
		if m.ID != "" {
			ids = append(ids, m.ID)
		}
	}
	return ids, nil
}

// ProbeModels validates credentials and returns the filtered catalogue.
// ErrInvalidKey is authoritative; ErrModelsUnavailable means the key could not
// be verified but might still work.
func ProbeModels(ctx context.Context, provider Provider, baseURL, key string) ([]string, error) {
	// OpenRouter's /models endpoint is public, so it cannot validate a key.
	// /key is authenticated and does.
	if strings.Contains(baseURL, "openrouter.ai") {
		if _, err := doRequest(ctx, baseURL, key, "/key"); err != nil {
			return nil, err
		}
	}
	live, err := FetchModels(ctx, baseURL, key)
	if err != nil {
		if errors.Is(err, ErrModelsUnavailable) {
			return FilterModels(provider, nil), err
		}
		return nil, err
	}
	return FilterModels(provider, live), nil
}
