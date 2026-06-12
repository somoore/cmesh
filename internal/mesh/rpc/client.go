package rpc

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"cmesh/internal/mesh/schema"
)

type Client struct {
	BaseURL string
	HTTP    *http.Client
}

func NewClient(baseURL string) *Client {
	return &Client{
		BaseURL: strings.TrimRight(baseURL, "/"),
		HTTP:    &http.Client{Timeout: 30 * time.Second},
	}
}

func (c *Client) Hello(ctx context.Context) (schema.HelloResponse, error) {
	var out schema.HelloResponse
	return out, c.do(ctx, http.MethodGet, "/v1/hello", nil, &out)
}

func (c *Client) CreateTask(ctx context.Context, req schema.CreateTaskRequest) (schema.CreateTaskResponse, error) {
	var out schema.CreateTaskResponse
	return out, c.do(ctx, http.MethodPost, "/v1/tasks", req, &out)
}

func (c *Client) TaskStatus(ctx context.Context, taskID string) (schema.TaskStatusResponse, error) {
	var out schema.TaskStatusResponse
	return out, c.do(ctx, http.MethodGet, "/v1/tasks/"+url.PathEscape(taskID), nil, &out)
}

func (c *Client) TaskResult(ctx context.Context, taskID string) (schema.TaskResultResponse, error) {
	var out schema.TaskResultResponse
	return out, c.do(ctx, http.MethodGet, "/v1/tasks/"+url.PathEscape(taskID)+"/result", nil, &out)
}

func (c *Client) Cancel(ctx context.Context, taskID, reason string) (schema.CancelTaskResponse, error) {
	var out schema.CancelTaskResponse
	return out, c.do(ctx, http.MethodPost, "/v1/tasks/"+url.PathEscape(taskID)+"/cancel", schema.CancelTaskRequest{Reason: reason}, &out)
}

func (c *Client) do(ctx context.Context, method, path string, in any, out any) error {
	var body *bytes.Reader
	if in != nil {
		encoded, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(encoded)
	} else {
		body = bytes.NewReader(nil)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.BaseURL+path, body)
	if err != nil {
		return err
	}
	req.Header.Set(schema.HeaderSchemaVersion, schema.Version)
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	httpClient := c.HTTP
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		var apiErr schema.Error
		if err := json.NewDecoder(resp.Body).Decode(&apiErr); err == nil && apiErr.Code != "" {
			return fmt.Errorf("%s: %s", apiErr.Code, apiErr.Detail)
		}
		return fmt.Errorf("mesh rpc %s %s: HTTP %d", method, path, resp.StatusCode)
	}
	return json.NewDecoder(resp.Body).Decode(out)
}
