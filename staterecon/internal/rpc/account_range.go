package rpc

import (
	"context"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	log "github.com/sirupsen/logrus"

	"github.com/whitechainio/whitechain-utils/staterecon/internal/models"
)

func (c *Client) AccountRange(ctx context.Context, block string, start string, maxResults int) (AccountRangePage, error) {
	if strings.TrimSpace(start) == "" {
		start = models.ZeroHash()
	} else {
		normalizedStart, err := models.NormalizeWord32(start)
		if err != nil {
			return AccountRangePage{}, fmt.Errorf("invalid account range start %q: %w", start, err)
		}
		start = normalizedStart
	}
	if strings.TrimSpace(block) == "" {
		block = "latest"
	}
	if maxResults <= 0 {
		maxResults = 1000
	}

	var raw json.RawMessage
	params := []any{block, start, maxResults, false, true, false}
	err := c.Call(ctx, "debug_accountRange", params, &raw)
	if err == nil {
		return parseAccountRangePage(raw)
	}

	return AccountRangePage{}, fmt.Errorf("debug_accountRange call failed: %w", err)
}

func parseAccountRangePage(raw json.RawMessage) (AccountRangePage, error) {
	var payload map[string]json.RawMessage
	if err := json.Unmarshal(raw, &payload); err != nil {
		return AccountRangePage{}, fmt.Errorf("decode account range payload: %w", err)
	}

	entries, ok := findAccountMap(payload)
	if !ok {
		return AccountRangePage{}, fmt.Errorf("account range response missing account map")
	}

	keys := make([]string, 0, len(entries))
	for key := range entries {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	accounts := make([]models.AccountSnapshot, 0, len(entries))
	for _, key := range keys {
		account, err := parseAccountEntry(key, entries[key])
		if err != nil {
			log.WithFields(log.Fields{
				"key":   key,
				"error": err,
			}).Warn("parse account entry failed")
			continue
		}
		if account == nil {
			continue
		}
		accounts = append(accounts, *account)
	}

	return AccountRangePage{
		Accounts: accounts,
		Next:     extractNextCursor(payload),
	}, nil
}

func findAccountMap(payload map[string]json.RawMessage) (map[string]json.RawMessage, bool) {
	for _, key := range []string{"accounts", "addressMap", "accountMap"} {
		if len(payload[key]) == 0 {
			continue
		}
		var entries map[string]json.RawMessage
		if err := json.Unmarshal(payload[key], &entries); err == nil {
			return entries, true
		}
	}
	return nil, false
}

func parseAccountEntry(key string, rawEntry json.RawMessage) (*models.AccountSnapshot, error) {
	var entry accountRangeEntry
	if err := json.Unmarshal(rawEntry, &entry); err != nil {
		return nil, fmt.Errorf("decode account entry %q: %w", key, err)
	}

	address := strings.TrimSpace(entry.Address)
	if address == "" {
		address = key
	}

	normalizedAddress, err := models.NormalizeAddress(address)
	if err != nil {
		return nil, fmt.Errorf("invalid account entry address, address: %s, normalized address %s", address, normalizedAddress)
	}

	balance, err := normalizeQuantityRaw(entry.Balance)
	if err != nil {
		return nil, fmt.Errorf("normalize balance for %s: %w", normalizedAddress, err)
	}

	nonce, err := normalizeQuantityRaw(entry.Nonce)
	if err != nil {
		return nil, fmt.Errorf("normalize nonce for %s: %w", normalizedAddress, err)
	}

	codeHash, err := models.NormalizeCodeHash(entry.CodeHash)
	if err != nil {
		return nil, fmt.Errorf("normalize code hash for %s: %w", normalizedAddress, err)
	}
	if codeHash == "" {
		codeHash = models.EmptyCodeHash()
	}

	var codePtr *string
	var codeForType string
	if entry.Code != nil {
		normalizedCode, normErr := models.NormalizeHexData(*entry.Code)
		if normErr != nil {
			return nil, fmt.Errorf("normalize code for %s: %w", normalizedAddress, normErr)
		}
		codeForType = normalizedCode
		codePtr = &normalizedCode
	}

	accountType := inferAccountType(codeHash, codeForType)
	snapshot := models.AccountSnapshot{
		Address:  normalizedAddress,
		Balance:  balance,
		Nonce:    nonce,
		CodeHash: codeHash,
		Code:     codePtr,
		Type:     accountType,
	}
	return &snapshot, nil
}

func normalizeQuantityRaw(raw json.RawMessage) (string, error) {
	value := strings.TrimSpace(string(raw))
	if value == "" || value == "null" {
		return "0x0", nil
	}
	if strings.HasPrefix(value, "\"") {
		var s string
		if err := json.Unmarshal(raw, &s); err != nil {
			return "", err
		}
		return models.NormalizeQuantity(s)
	}

	return models.NormalizeQuantity(value)
}

func extractNextCursor(payload map[string]json.RawMessage) string {
	for _, key := range []string{"next", "nextKey"} {
		if len(payload[key]) == 0 {
			continue
		}
		var cursor string
		if err := json.Unmarshal(payload[key], &cursor); err == nil && strings.TrimSpace(cursor) != "" {
			normalized, ok := normalizeAccountRangeCursor(cursor)
			if ok {
				return normalized
			}
		}
	}
	return ""
}

func normalizeAccountRangeCursor(cursor string) (string, bool) {
	trimmed := strings.TrimSpace(cursor)
	if trimmed == "" {
		return "", false
	}

	normalized, err := models.NormalizeWord32(trimmed)
	if err == nil {
		return normalized, true
	}

	decoded, decodeErr := decodeBase64Cursor(trimmed)
	if decodeErr != nil || len(decoded) == 0 {
		return "", false
	}

	normalized, normErr := models.NormalizeWord32("0x" + hex.EncodeToString(decoded))
	if normErr != nil {
		return "", false
	}
	return normalized, true
}

func decodeBase64Cursor(value string) ([]byte, error) {
	for _, enc := range []*base64.Encoding{
		base64.StdEncoding,
		base64.RawStdEncoding,
		base64.URLEncoding,
		base64.RawURLEncoding,
	} {
		decoded, err := enc.DecodeString(value)
		if err == nil {
			return decoded, nil
		}
	}
	return nil, fmt.Errorf("unsupported cursor encoding")
}

func inferAccountType(codeHash string, code string) models.AccountType {
	if !models.IsZeroCode(code) {
		return models.AccountTypeContract
	}
	switch strings.ToLower(codeHash) {
	case "", models.ZeroHash(), models.EmptyCodeHash():
		return models.AccountTypeEOA
	default:
		return models.AccountTypeContract
	}
}
