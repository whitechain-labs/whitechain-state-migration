package rpc

import (
	"context"
	"fmt"
	"net/http"
	urlpkg "net/url"
	"strings"
	"time"

	gethrpc "github.com/ethereum/go-ethereum/rpc"
)

func NewClient(url string, timeout time.Duration, batchSize int) *Client {
	if batchSize <= 0 {
		batchSize = 1
	}
	trimmedURL := strings.TrimSpace(url)

	client := &Client{
		url:       trimmedURL,
		batchSize: batchSize,
	}
	if trimmedURL == "" {
		client.initErr = fmt.Errorf("rpc url is empty")
		return client
	}

	parsedURL, parseErr := urlpkg.Parse(trimmedURL)
	if parseErr != nil {
		client.initErr = fmt.Errorf("parse rpc url %q: %w", trimmedURL, parseErr)
		return client
	}

	options := []gethrpc.ClientOption{}
	if parsedURL.Scheme == "http" || parsedURL.Scheme == "https" {
		options = append(options, gethrpc.WithHTTPClient(&http.Client{Timeout: timeout}))
	}

	dialCtx := context.Background()
	cancel := func() {}
	if timeout > 0 {
		dialCtx, cancel = context.WithTimeout(context.Background(), timeout)
	}
	defer cancel()

	rpcClient, err := gethrpc.DialOptions(dialCtx, trimmedURL, options...)
	if err != nil {
		client.initErr = fmt.Errorf("dial rpc endpoint %s: %w", trimmedURL, err)
		return client
	}

	client.rpc = rpcClient
	return client
}

func (c *Client) Call(ctx context.Context, method string, params any, out any) error {
	if err := c.clientError(); err != nil {
		return err
	}
	return c.rpc.CallContext(ctx, out, method, normalizeArgs(params)...)
}

func (c *Client) BatchCall(ctx context.Context, elems []BatchElem) error {
	if len(elems) == 0 {
		return nil
	}
	if err := c.clientError(); err != nil {
		for i := range elems {
			elems[i].Err = err
		}
		return err
	}

	var firstErr error
	for start := 0; start < len(elems); start += c.batchSize {
		end := start + c.batchSize
		if end > len(elems) {
			end = len(elems)
		}
		if err := c.doBatch(ctx, elems[start:end]); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

func (c *Client) doBatch(ctx context.Context, elems []BatchElem) error {
	batch := make([]gethrpc.BatchElem, len(elems))
	for i := range elems {
		elems[i].Err = nil
		batch[i] = gethrpc.BatchElem{
			Method: elems[i].Method,
			Args:   normalizeArgs(elems[i].Params),
			Result: elems[i].Result,
		}
	}

	err := c.rpc.BatchCallContext(ctx, batch)
	for i := range elems {
		elems[i].Err = batch[i].Error
	}

	if err != nil {
		for i := range elems {
			if elems[i].Err == nil {
				elems[i].Err = err
			}
		}
		return err
	}
	return nil
}

func (c *Client) clientError() error {
	if c == nil {
		return fmt.Errorf("rpc client is nil")
	}
	if c.initErr != nil {
		return c.initErr
	}
	if c.rpc == nil {
		return fmt.Errorf("rpc client for %s is not initialized", c.url)
	}
	return nil
}

func normalizeArgs(params any) []any {
	switch value := params.(type) {
	case nil:
		return nil
	case []any:
		return value
	default:
		return []any{value}
	}
}
