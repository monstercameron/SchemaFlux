package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// FetchTool performs HTTP GET requests.
var FetchTool = &Tool{
	Name:        "fetch",
	Description: "Make HTTP GET request to a URL and return the response",
	Category:    CategoryHTTP,
	Parameters: ObjectSchema(map[string]ParameterSchema{
		"url":     StringParam("URL to fetch"),
		"headers": StringParam("Optional JSON object of headers"),
		"timeout": NumberParam("Timeout in seconds (default: 30)"),
	}, []string{"url"}),
	Execute: executeFetch,
}

// PostTool performs HTTP POST requests.
var PostTool = &Tool{
	Name:        "post",
	Description: "Make HTTP POST request to a URL with a body",
	Category:    CategoryHTTP,
	Parameters: ObjectSchema(map[string]ParameterSchema{
		"url":          StringParam("URL to post to"),
		"body":         StringParam("Request body (string or JSON)"),
		"content_type": StringParam("Content-Type header (default: application/json)"),
		"headers":      StringParam("Optional JSON object of additional headers"),
		"timeout":      NumberParam("Timeout in seconds (default: 30)"),
	}, []string{"url"}),
	Execute: executePost,
}

// HTTPResponse represents the response from an HTTP request.
type HTTPResponse struct {
	StatusCode int               `json:"status_code"`
	Status     string            `json:"status"`
	Headers    map[string]string `json:"headers"`
	Body       string            `json:"body"`
	BodyJSON   any               `json:"body_json,omitempty"`
}

func executeFetch(ctx context.Context, params map[string]any) (Result, error) {
	urlStr, _ := params["url"].(string)
	if urlStr == "" {
		return ErrorResultFromError(fmt.Errorf("url is required")), nil
	}

	timeout := 30.0
	if t, ok := params["timeout"].(float64); ok {
		timeout = t
	}

	headers := make(map[string]string)
	if h, ok := params["headers"].(string); ok && h != "" {
		_ = json.Unmarshal([]byte(h), &headers)
	}

	resp, err := Fetch(ctx, urlStr, headers, time.Duration(timeout)*time.Second)
	if err != nil {
		return ErrorResult(err), nil
	}

	return NewResult(resp), nil
}

func executePost(ctx context.Context, params map[string]any) (Result, error) {
	urlStr, _ := params["url"].(string)
	if urlStr == "" {
		return ErrorResultFromError(fmt.Errorf("url is required")), nil
	}

	body, _ := params["body"].(string)
	contentType := "application/json"
	if ct, ok := params["content_type"].(string); ok && ct != "" {
		contentType = ct
	}

	timeout := 30.0
	if t, ok := params["timeout"].(float64); ok {
		timeout = t
	}

	headers := map[string]string{"Content-Type": contentType}
	if h, ok := params["headers"].(string); ok && h != "" {
		var extra map[string]string
		_ = json.Unmarshal([]byte(h), &extra)
		for k, v := range extra {
			headers[k] = v
		}
	}

	resp, err := Post(ctx, urlStr, body, headers, time.Duration(timeout)*time.Second)
	if err != nil {
		return ErrorResult(err), nil
	}

	return NewResult(resp), nil
}

// Fetch performs an HTTP GET request.
func Fetch(ctx context.Context, urlStr string, headers map[string]string, timeout time.Duration) (*HTTPResponse, error) {
	return doRequest(ctx, "GET", urlStr, "", headers, timeout)
}

// Post performs an HTTP POST request.
func Post(ctx context.Context, urlStr, body string, headers map[string]string, timeout time.Duration) (*HTTPResponse, error) {
	return doRequest(ctx, "POST", urlStr, body, headers, timeout)
}

func doRequest(ctx context.Context, method, urlStr, body string, headers map[string]string, timeout time.Duration) (*HTTPResponse, error) {
	// Validate URL
	_, err := url.Parse(urlStr)
	if err != nil {
		return nil, fmt.Errorf("invalid URL: %w", err)
	}

	client := &http.Client{Timeout: timeout}

	var bodyReader io.Reader
	if body != "" {
		bodyReader = strings.NewReader(body)
	}

	req, err := http.NewRequestWithContext(ctx, method, urlStr, bodyReader)
	if err != nil {
		return nil, err
	}

	for k, v := range headers {
		req.Header.Set(k, v)
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	result := &HTTPResponse{
		StatusCode: resp.StatusCode,
		Status:     resp.Status,
		Headers:    make(map[string]string),
		Body:       string(respBody),
	}

	for k, v := range resp.Header {
		if len(v) > 0 {
			result.Headers[k] = v[0]
		}
	}

	// Try to parse as JSON
	var jsonBody any
	if err := json.Unmarshal(respBody, &jsonBody); err == nil {
		result.BodyJSON = jsonBody
	}

	return result, nil
}

// WebhookTool manages webhooks.
var WebhookTool = &Tool{
	Name:        "webhook",
	Description: "Trigger a webhook with custom payload",
	Category:    CategoryHTTP,
	Parameters: ObjectSchema(map[string]ParameterSchema{
		"url":     StringParam("Webhook URL"),
		"payload": StringParam("JSON payload to send"),
		"method":  EnumParam("HTTP method", []string{"POST", "PUT", "PATCH"}),
	}, []string{"url", "payload"}),
	Execute: executeWebhook,
}

func executeWebhook(ctx context.Context, params map[string]any) (Result, error) {
	urlStr, _ := params["url"].(string)
	payload, _ := params["payload"].(string)
	method := "POST"
	if m, ok := params["method"].(string); ok {
		method = m
	}

	// Validate JSON payload
	var jsonPayload any
	if err := json.Unmarshal([]byte(payload), &jsonPayload); err != nil {
		return ErrorResultFromError(fmt.Errorf("invalid JSON payload: %w", err)), nil
	}

	resp, err := doRequest(ctx, method, urlStr, payload, map[string]string{
		"Content-Type": "application/json",
	}, 30*time.Second)
	if err != nil {
		return ErrorResult(err), nil
	}

	return NewResultWithMeta(resp, map[string]any{
		"webhook": urlStr,
		"method":  method,
	}), nil
}

// EncodeURLTool encodes/decodes URLs.
var EncodeURLTool = &Tool{
	Name:        "encode_url",
	Description: "Encode or decode URL components",
	Category:    CategoryHTTP,
	Parameters: ObjectSchema(map[string]ParameterSchema{
		"action": EnumParam("Action to perform", []string{"encode", "decode"}),
		"text":   StringParam("Text to encode/decode"),
	}, []string{"action", "text"}),
	Execute: executeEncodeURL,
}

func executeEncodeURL(ctx context.Context, params map[string]any) (Result, error) {
	action, _ := params["action"].(string)
	text, _ := params["text"].(string)

	var result string
	switch action {
	case "encode":
		result = url.QueryEscape(text)
	case "decode":
		var err error
		result, err = url.QueryUnescape(text)
		if err != nil {
			return ErrorResult(err), nil
		}
	default:
		return ErrorResultFromError(fmt.Errorf("unknown action: %s", action)), nil
	}

	return NewResult(result), nil
}

// BuildURLTool builds URLs with query parameters.
var BuildURLTool = &Tool{
	Name:        "build_url",
	Description: "Build a URL with query parameters",
	Category:    CategoryHTTP,
	Parameters: ObjectSchema(map[string]ParameterSchema{
		"base":   StringParam("Base URL"),
		"params": StringParam("JSON object of query parameters"),
	}, []string{"base"}),
	Execute: executeBuildURL,
}

func executeBuildURL(ctx context.Context, params map[string]any) (Result, error) {
	base, _ := params["base"].(string)
	paramsJSON, _ := params["params"].(string)

	u, err := url.Parse(base)
	if err != nil {
		return ErrorResultFromError(fmt.Errorf("invalid base URL: %w", err)), nil
	}

	if paramsJSON != "" {
		var queryParams map[string]string
		if err := json.Unmarshal([]byte(paramsJSON), &queryParams); err != nil {
			return ErrorResultFromError(fmt.Errorf("invalid params JSON: %w", err)), nil
		}

		q := u.Query()
		for k, v := range queryParams {
			q.Set(k, v)
		}
		u.RawQuery = q.Encode()
	}

	return NewResult(u.String()), nil
}

// JSONRequest helper for making JSON API calls
func JSONRequest(ctx context.Context, method, urlStr string, body any, headers map[string]string) (*HTTPResponse, error) {
	var bodyStr string
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("failed to marshal body: %w", err)
		}
		bodyStr = string(b)
	}

	if headers == nil {
		headers = make(map[string]string)
	}
	headers["Content-Type"] = "application/json"
	headers["Accept"] = "application/json"

	return doRequest(ctx, method, urlStr, bodyStr, headers, 30*time.Second)
}

// DownloadFile downloads a file from URL to bytes
func DownloadFile(ctx context.Context, urlStr string, timeout time.Duration) ([]byte, string, error) {
	client := &http.Client{Timeout: timeout}
	req, err := http.NewRequestWithContext(ctx, "GET", urlStr, nil)
	if err != nil {
		return nil, "", err
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, "", fmt.Errorf("HTTP %d: %s", resp.StatusCode, resp.Status)
	}

	var buf bytes.Buffer
	if _, err := io.Copy(&buf, resp.Body); err != nil {
		return nil, "", err
	}

	contentType := resp.Header.Get("Content-Type")
	return buf.Bytes(), contentType, nil
}

func init() {
	mustRegister(FetchTool)
	mustRegister(PostTool)
	mustRegister(WebhookTool)
	mustRegister(EncodeURLTool)
	mustRegister(BuildURLTool)
}
