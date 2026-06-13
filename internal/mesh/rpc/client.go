package rpc

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"cmesh/internal/mesh/schema"
	"cmesh/internal/task"
)

type Client struct {
	BaseURL string
	HTTP    *http.Client
}

type Error struct {
	StatusCode int
	Code       task.ErrorCode
	Detail     string
}

func (e *Error) Error() string {
	if e == nil {
		return ""
	}
	if e.Detail == "" {
		return string(e.Code)
	}
	return fmt.Sprintf("%s: %s", e.Code, e.Detail)
}

func CodeOf(err error) task.ErrorCode {
	var rpcErr *Error
	if errors.As(err, &rpcErr) {
		return rpcErr.Code
	}
	return ""
}

func NewClient(baseURL string) *Client {
	return &Client{
		BaseURL: strings.TrimRight(baseURL, "/"),
		HTTP:    &http.Client{},
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

func (c *Client) TaskStatus(ctx context.Context, taskID string, wait bool, timeoutSeconds int) (schema.TaskStatusResponse, error) {
	var out schema.TaskStatusResponse
	path := "/v1/tasks/" + url.PathEscape(taskID)
	if wait {
		q := url.Values{}
		q.Set("wait", "true")
		if timeoutSeconds > 0 {
			q.Set("timeout_s", strconv.Itoa(timeoutSeconds))
			var cancel context.CancelFunc
			ctx, cancel = context.WithTimeout(ctx, time.Duration(timeoutSeconds+10)*time.Second)
			defer cancel()
		}
		path += "?" + q.Encode()
	}
	return out, c.do(ctx, http.MethodGet, path, nil, &out)
}

func (c *Client) TaskResult(ctx context.Context, taskID string) (schema.TaskResultResponse, error) {
	var out schema.TaskResultResponse
	return out, c.do(ctx, http.MethodGet, "/v1/tasks/"+url.PathEscape(taskID)+"/result", nil, &out)
}

func (c *Client) Cancel(ctx context.Context, taskID, reason string) (schema.CancelTaskResponse, error) {
	var out schema.CancelTaskResponse
	return out, c.do(ctx, http.MethodPost, "/v1/tasks/"+url.PathEscape(taskID)+"/cancel", schema.CancelTaskRequest{Reason: reason}, &out)
}

func (c *Client) ListTasks(ctx context.Context, limit int) (schema.TaskListResponse, error) {
	var out schema.TaskListResponse
	path := "/v1/tasks"
	if limit > 0 {
		path += "?limit=" + url.QueryEscape(strconv.Itoa(limit))
	}
	return out, c.do(ctx, http.MethodGet, path, nil, &out)
}

func (c *Client) TaskTranscript(ctx context.Context, taskID string) (schema.TaskTranscriptResponse, error) {
	var out schema.TaskTranscriptResponse
	return out, c.do(ctx, http.MethodGet, "/v1/tasks/"+url.PathEscape(taskID)+"/transcript", nil, &out)
}

func (c *Client) TaskArtifact(ctx context.Context, taskID, kind string) ([]byte, error) {
	if kind == "" {
		kind = "git-bundle"
	}
	return c.doBytes(ctx, http.MethodGet, "/v1/tasks/"+url.PathEscape(taskID)+"/artifacts/"+url.PathEscape(kind), nil)
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
		body, readErr := io.ReadAll(resp.Body)
		if readErr == nil {
			if rpcErr := errorFromBody(resp.StatusCode, body); rpcErr != nil {
				return rpcErr
			}
		}
		return fmt.Errorf("mesh rpc %s %s: HTTP %d", method, path, resp.StatusCode)
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

func (c *Client) doBytes(ctx context.Context, method, path string, in any) ([]byte, error) {
	var body *bytes.Reader
	if in != nil {
		encoded, err := json.Marshal(in)
		if err != nil {
			return nil, err
		}
		body = bytes.NewReader(encoded)
	} else {
		body = bytes.NewReader(nil)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.BaseURL+path, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set(schema.HeaderSchemaVersion, schema.Version)
	httpClient := c.HTTP
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		body, readErr := io.ReadAll(resp.Body)
		if readErr == nil {
			if rpcErr := errorFromBody(resp.StatusCode, body); rpcErr != nil {
				return nil, rpcErr
			}
		}
		return nil, fmt.Errorf("mesh rpc %s %s: HTTP %d", method, path, resp.StatusCode)
	}
	return io.ReadAll(resp.Body)
}

func errorFromBody(status int, body []byte) *Error {
	var apiErr schema.Error
	if err := json.Unmarshal(body, &apiErr); err == nil && apiErr.Code != "" {
		return &Error{StatusCode: status, Code: apiErr.Code, Detail: apiErr.Detail}
	}
	var wrapped struct {
		Error *schema.Error `json:"error"`
	}
	if err := json.Unmarshal(body, &wrapped); err == nil && wrapped.Error != nil && wrapped.Error.Code != "" {
		return &Error{StatusCode: status, Code: wrapped.Error.Code, Detail: wrapped.Error.Detail}
	}
	return nil
}
