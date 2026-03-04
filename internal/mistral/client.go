package mistral

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

type Client struct {
	HTTP    *http.Client
	BaseURL string
	APIKey  string
}

func NewClient(baseURL, apiKey string) *Client {
	return &Client{
		HTTP:    &http.Client{Timeout: 8 * time.Minute},
		BaseURL: strings.TrimRight(baseURL, "/"),
		APIKey:  strings.TrimSpace(apiKey),
	}
}

func (c *Client) IsConfigured() bool {
	return c.APIKey != "" && c.BaseURL != ""
}

func (c *Client) DoJSON(ctx context.Context, method, path string, body []byte) (int, []byte, error) {
	if !c.IsConfigured() {
		return http.StatusServiceUnavailable, nil, errors.New("mistral client is not configured")
	}
	if len(body) == 0 {
		body = []byte("{}")
	}
	req, err := http.NewRequestWithContext(ctx, method, c.BaseURL+path, bytes.NewReader(body))
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.APIKey)
	req.Header.Set("Content-Type", "application/json")

	res, err := c.HTTP.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer res.Body.Close()
	data, err := io.ReadAll(res.Body)
	if err != nil {
		return 0, nil, err
	}
	return res.StatusCode, data, nil
}

func (c *Client) DoMultipart(ctx context.Context, path string, body []byte, contentType string) (int, []byte, error) {
	if !c.IsConfigured() {
		return http.StatusServiceUnavailable, nil, errors.New("mistral client is not configured")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+path, bytes.NewReader(body))
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.APIKey)
	if strings.TrimSpace(contentType) == "" {
		return 0, nil, fmt.Errorf("missing content-type for multipart request")
	}
	req.Header.Set("Content-Type", contentType)
	res, err := c.HTTP.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer res.Body.Close()
	data, err := io.ReadAll(res.Body)
	if err != nil {
		return 0, nil, err
	}
	return res.StatusCode, data, nil
}

func (c *Client) DoGet(ctx context.Context, path string) (int, []byte, error) {
	if !c.IsConfigured() {
		return http.StatusServiceUnavailable, nil, errors.New("mistral client is not configured")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.BaseURL+path, nil)
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.APIKey)
	res, err := c.HTTP.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer res.Body.Close()
	data, err := io.ReadAll(res.Body)
	if err != nil {
		return 0, nil, err
	}
	return res.StatusCode, data, nil
}
