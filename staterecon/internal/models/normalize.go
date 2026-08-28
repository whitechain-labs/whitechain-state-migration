package models

import (
	"encoding/hex"
	"fmt"
	"math/big"
	"strings"
)

const (
	emptyCodeHash = "0xc5d2460186f7233c927e7db2dcc703c0e500b653ca82273b7bfad8045d85a470"
	zeroHash      = "0x0000000000000000000000000000000000000000000000000000000000000000"
)

func EmptyCodeHash() string {
	return emptyCodeHash
}

func ZeroHash() string {
	return zeroHash
}

func NormalizeAddress(value string) (string, error) {
	trimmed := strings.TrimSpace(strings.ToLower(value))
	if trimmed == "" {
		return "", fmt.Errorf("address is empty")
	}

	trimmed = strings.TrimPrefix(trimmed, "0x")

	if len(trimmed) != 40 {
		return "", fmt.Errorf("address %q must have 40 hex chars", value)
	}

	if _, err := hex.DecodeString(trimmed); err != nil {
		return "", fmt.Errorf("address %q is not valid hex: %w", value, err)
	}

	return "0x" + trimmed, nil
}

func NormalizeHexData(value string) (string, error) {
	trimmed := strings.TrimSpace(strings.ToLower(value))
	if trimmed == "" {
		return "0x", nil
	}

	trimmed = strings.TrimPrefix(trimmed, "0x")

	if trimmed == "" {
		return "0x", nil
	}

	if len(trimmed)%2 != 0 {
		trimmed = "0" + trimmed
	}

	if _, err := hex.DecodeString(trimmed); err != nil {
		return "", fmt.Errorf("hex data %q is invalid: %w", value, err)
	}

	return "0x" + trimmed, nil
}

func NormalizeQuantity(value string) (string, error) {
	trimmed := strings.TrimSpace(strings.ToLower(value))
	if trimmed == "" || trimmed == "null" {
		return "0x0", nil
	}

	base := 10
	if strings.HasPrefix(trimmed, "0x") {
		trimmed = strings.TrimPrefix(trimmed, "0x")
		if trimmed == "" {
			return "0x0", nil
		}
		base = 16
	}

	n := new(big.Int)
	if _, ok := n.SetString(trimmed, base); !ok {
		return "", fmt.Errorf("invalid quantity %q", value)
	}

	if n.Sign() < 0 {
		return "", fmt.Errorf("negative quantity %q", value)
	}

	return "0x" + n.Text(16), nil
}

func NormalizeCodeHash(value string) (string, error) {
	trimmed := strings.TrimSpace(strings.ToLower(value))
	if trimmed == "" || trimmed == "null" {
		return "", nil
	}

	trimmed = strings.TrimPrefix(trimmed, "0x")

	if trimmed == "" {
		return "", nil
	}

	if len(trimmed)%2 != 0 {
		trimmed = "0" + trimmed
	}

	if _, err := hex.DecodeString(trimmed); err != nil {
		return "", fmt.Errorf("invalid code hash %q: %w", value, err)
	}

	if len(trimmed) > 64 {
		trimmed = trimmed[len(trimmed)-64:]
	}
	if len(trimmed) < 64 {
		trimmed = strings.Repeat("0", 64-len(trimmed)) + trimmed
	}

	return "0x" + trimmed, nil
}

func NormalizeWord32(value string) (string, error) {
	trimmed := strings.TrimSpace(strings.ToLower(value))
	if trimmed == "" || trimmed == "null" {
		return zeroHash, nil
	}

	trimmed = strings.TrimPrefix(trimmed, "0x")

	if trimmed == "" {
		return zeroHash, nil
	}

	if len(trimmed)%2 != 0 {
		trimmed = "0" + trimmed
	}

	if _, err := hex.DecodeString(trimmed); err != nil {
		return "", fmt.Errorf("invalid 32-byte word %q: %w", value, err)
	}

	if len(trimmed) > 64 {
		trimmed = trimmed[len(trimmed)-64:]
	}
	if len(trimmed) < 64 {
		trimmed = strings.Repeat("0", 64-len(trimmed)) + trimmed
	}

	return "0x" + trimmed, nil
}

func IsZeroCode(code string) bool {
	normalized, err := NormalizeHexData(code)
	if err != nil {
		return true
	}

	return normalized == "0x"
}
