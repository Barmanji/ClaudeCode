// Package lsp talks to an LSP bridge server over HTTP. The bridge itself is
// provided by the CodeCrafters harness via CODECRAFTERS_LSP_URL; without it the
// LSP tool is simply not offered to the model.
package lsp

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"
)

// Client is an HTTP JSON-RPC bridge.
type Client struct {
	url    string
	client *http.Client
}

// NewClient connects to the bridge described by CODECRAFTERS_LSP_URL.
func NewClient() (*Client, error) {
	url := os.Getenv("CODECRAFTERS_LSP_URL")
	if url == "" {
		return nil, fmt.Errorf("CODECRAFTERS_LSP_URL is not set")
	}
	return &Client{
		url:    url,
		client: &http.Client{Timeout: 10 * time.Second},
	}, nil
}

// Call sends an action to the bridge and returns its raw response.
func (l *Client) Call(action string, payload any) (json.RawMessage, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	response, err := l.client.Post(l.url+"/"+action, "application/json", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()

	data, err := io.ReadAll(response.Body)
	if err != nil {
		return nil, err
	}
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("LSP transport: %s", data)
	}
	return json.RawMessage(data), nil
}
