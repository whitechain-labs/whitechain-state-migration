package tokensync

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/whitechain-labs/whitechain-state-migration/staterecon/internal/models"
)

type explorerClient struct {
	baseURL    *url.URL
	httpClient *http.Client
}

type explorerTokenPayload struct {
	Type        string     `json:"type"`
	Contract    string     `json:"contract"`
	Name        string     `json:"name"`
	Symbol      string     `json:"symbol"`
	TotalSupply jsonString `json:"totalSupply"`
	Decimals    int        `json:"decimals"`
}

type jsonString string

func (s *jsonString) UnmarshalJSON(data []byte) error {
	trimmed := strings.TrimSpace(string(data))
	if trimmed == "null" {
		*s = ""
		return nil
	}

	var asString string
	if err := json.Unmarshal(data, &asString); err == nil {
		*s = jsonString(asString)
		return nil
	}

	var asNumber json.Number
	if err := json.Unmarshal(data, &asNumber); err == nil {
		*s = jsonString(asNumber.String())
		return nil
	}

	return fmt.Errorf("unsupported JSON string value %q", trimmed)
}

func newExplorerClient(rawURL string, timeout time.Duration) (*explorerClient, error) {
	trimmed := strings.TrimSpace(rawURL)
	if trimmed == "" {
		return nil, fmt.Errorf("old explorer url is empty")
	}

	parsedURL, err := url.Parse(trimmed)
	if err != nil {
		return nil, fmt.Errorf("parse old explorer url %q: %w", trimmed, err)
	}
	if parsedURL.Scheme == "" || parsedURL.Host == "" {
		return nil, fmt.Errorf("old explorer url %q must include scheme and host", trimmed)
	}

	return &explorerClient{
		baseURL: parsedURL,
		httpClient: &http.Client{
			Timeout: timeout,
		},
	}, nil
}

func (c *explorerClient) FetchToken(ctx context.Context, address string) (*explorerToken, error) {
	if c == nil || c.baseURL == nil {
		return nil, fmt.Errorf("explorer client is not initialized")
	}

	req, err := c.newFetchTokenRequest(ctx, address)
	if err != nil {
		return nil, err
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request legacy explorer: %w", err)
	}
	defer resp.Body.Close()

	if err := validateExplorerResponse(resp); err != nil {
		return nil, err
	}

	payload, err := decodeExplorerTokenPayload(resp.Body)
	if err != nil {
		return nil, err
	}

	if err := validateExplorerContract(payload.Contract, address); err != nil {
		return nil, err
	}

	return &explorerToken{
		Type:        payload.Type,
		Contract:    payload.Contract,
		Name:        payload.Name,
		Symbol:      payload.Symbol,
		TotalSupply: string(payload.TotalSupply),
		Decimals:    payload.Decimals,
	}, nil
}

func (c *explorerClient) newFetchTokenRequest(ctx context.Context, address string) (*http.Request, error) {
	endpoint := *c.baseURL
	endpoint.Path = strings.TrimRight(endpoint.Path, "/") + "/token"

	query := endpoint.Query()
	query.Set("address", address)
	endpoint.RawQuery = query.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("build explorer request: %w", err)
	}
	return req, nil
}

func validateExplorerResponse(resp *http.Response) error {
	if resp.StatusCode == http.StatusOK {
		return nil
	}

	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	return fmt.Errorf("legacy explorer returned %s: %s", resp.Status, strings.TrimSpace(string(body)))
}

func decodeExplorerTokenPayload(body io.Reader) (*explorerTokenPayload, error) {
	var payload explorerTokenPayload
	if err := json.NewDecoder(body).Decode(&payload); err != nil {
		return nil, fmt.Errorf("decode legacy explorer response: %w", err)
	}
	return &payload, nil
}

func validateExplorerContract(contract, address string) error {
	if contract == "" {
		return nil
	}

	normalizedContract, err := models.NormalizeAddress(contract)
	if err != nil {
		return fmt.Errorf("legacy explorer returned invalid contract address %q: %w", contract, err)
	}
	normalizedAddress, err := models.NormalizeAddress(address)
	if err != nil {
		return fmt.Errorf("requested token address %q is invalid: %w", address, err)
	}
	if normalizedContract != normalizedAddress {
		return fmt.Errorf("legacy explorer returned contract %s for requested token %s", contract, address)
	}

	return nil
}
