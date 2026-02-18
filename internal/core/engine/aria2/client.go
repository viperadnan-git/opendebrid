package aria2

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sync/atomic"
	"time"
)

// Client is an aria2 JSON-RPC client.
type Client struct {
	url    string
	secret string
	nextID atomic.Int64
	http   *http.Client
}

func NewClient(url, secret string) *Client {
	return &Client{
		url:    url,
		secret: secret,
		http:   &http.Client{Timeout: 30 * time.Second},
	}
}

func (c *Client) call(ctx context.Context, method string, params ...any) (json.RawMessage, error) {
	// Prepend secret token if set
	allParams := make([]any, 0, len(params)+1)
	if c.secret != "" {
		allParams = append(allParams, "token:"+c.secret)
	}
	allParams = append(allParams, params...)

	reqBody := rpcRequest{
		JSONRPC: "2.0",
		ID:      fmt.Sprintf("%d", c.nextID.Add(1)),
		Method:  method,
		Params:  allParams,
	}

	body, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.url, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("rpc call: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}

	var rpcResp rpcResponse
	if err := json.Unmarshal(respBody, &rpcResp); err != nil {
		return nil, fmt.Errorf("unmarshal response: %w", err)
	}

	if rpcResp.Error != nil {
		return nil, fmt.Errorf("aria2 rpc error %d: %s", rpcResp.Error.Code, rpcResp.Error.Message)
	}

	raw, err := json.Marshal(rpcResp.Result)
	if err != nil {
		return nil, fmt.Errorf("re-marshal result: %w", err)
	}
	return raw, nil
}

func (c *Client) AddURI(ctx context.Context, uris []string, opts map[string]string) (string, error) {
	raw, err := c.call(ctx, "aria2.addUri", uris, opts)
	if err != nil {
		return "", err
	}
	var gid string
	if err := json.Unmarshal(raw, &gid); err != nil {
		return "", fmt.Errorf("parse gid: %w", err)
	}
	return gid, nil
}

func (c *Client) Remove(ctx context.Context, gid string) error {
	_, err := c.call(ctx, "aria2.remove", gid)
	return err
}

func (c *Client) ForceRemove(ctx context.Context, gid string) error {
	_, err := c.call(ctx, "aria2.forceRemove", gid)
	return err
}

func (c *Client) RemoveDownloadResult(ctx context.Context, gid string) error {
	_, err := c.call(ctx, "aria2.removeDownloadResult", gid)
	return err
}

func (c *Client) TellActive(ctx context.Context) ([]*statusResponse, error) {
	raw, err := c.call(ctx, "aria2.tellActive")
	if err != nil {
		return nil, err
	}
	var statuses []*statusResponse
	if err := json.Unmarshal(raw, &statuses); err != nil {
		return nil, fmt.Errorf("parse active statuses: %w", err)
	}
	return statuses, nil
}

func (c *Client) TellStatus(ctx context.Context, gid string) (*statusResponse, error) {
	raw, err := c.call(ctx, "aria2.tellStatus", gid)
	if err != nil {
		return nil, err
	}
	var status statusResponse
	if err := json.Unmarshal(raw, &status); err != nil {
		return nil, fmt.Errorf("parse status: %w", err)
	}
	return &status, nil
}

func (c *Client) GetVersion(ctx context.Context) (string, error) {
	raw, err := c.call(ctx, "aria2.getVersion")
	if err != nil {
		return "", err
	}
	var result struct {
		Version string `json:"version"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		return "", err
	}
	return result.Version, nil
}
