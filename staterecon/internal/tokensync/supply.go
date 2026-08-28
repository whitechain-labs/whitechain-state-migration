package tokensync

import (
	"fmt"
	"strings"
)

const maxTokenDecimals = 255

func convertTotalSupply(value string, decimals int) (string, error) {
	if err := validateDecimals(decimals); err != nil {
		return "", err
	}

	trimmed, err := normalizeTotalSupplyInput(value)
	if err != nil {
		return "", err
	}
	if trimmed == "" {
		// return zero if not set or empty
		return "0", nil
	}

	whole, fractional, err := splitTotalSupply(trimmed, value)
	if err != nil {
		return "", err
	}
	if err := validateTotalSupplyDigits(value, whole, fractional); err != nil {
		return "", err
	}

	scaledFractional, err := scaleFractionalTotalSupply(value, fractional, decimals)
	if err != nil {
		return "", err
	}

	return normalizeSupplyDigits(whole, scaledFractional), nil
}

func validateDecimals(decimals int) error {
	if decimals < 0 {
		return fmt.Errorf("decimals must be non-negative, got %d", decimals)
	}
	if decimals > maxTokenDecimals {
		return fmt.Errorf("decimals must be between 0 and %d, got %d", maxTokenDecimals, decimals)
	}
	return nil
}

func normalizeTotalSupplyInput(value string) (string, error) {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return "", nil
	}
	if strings.HasPrefix(trimmed, "-") {
		return "", fmt.Errorf("totalSupply %q must not be negative", value)
	}
	if strings.ContainsAny(trimmed, "eE") {
		return "", fmt.Errorf("totalSupply %q uses unsupported exponent notation", value)
	}
	return strings.TrimPrefix(trimmed, "+"), nil
}

func splitTotalSupply(trimmed, raw string) (string, string, error) {
	parts := strings.Split(trimmed, ".")
	if len(parts) > 2 {
		return "", "", fmt.Errorf("invalid totalSupply %q", raw)
	}

	whole := parts[0]
	fractional := ""
	if len(parts) == 2 {
		fractional = parts[1]
	}
	if whole == "" && fractional == "" {
		return "", "", fmt.Errorf("invalid totalSupply %q", raw)
	}
	if whole == "" {
		whole = "0"
	}

	return whole, fractional, nil
}

func validateTotalSupplyDigits(raw, whole, fractional string) error {
	if !isDecimalDigits(whole) || !isDecimalDigits(fractional) {
		return fmt.Errorf("totalSupply %q is not a base-10 number", raw)
	}
	return nil
}

func scaleFractionalTotalSupply(raw, fractional string, decimals int) (string, error) {
	if len(fractional) > decimals {
		extra := fractional[decimals:]
		if strings.Trim(extra, "0") != "" {
			return "", fmt.Errorf("totalSupply %q has more fractional digits than decimals %d", raw, decimals)
		}
		fractional = fractional[:decimals]
	}

	return fractional + strings.Repeat("0", decimals-len(fractional)), nil
}

func normalizeSupplyDigits(whole, fractional string) string {
	normalized := strings.TrimLeft(whole+fractional, "0")
	if normalized == "" {
		return "0"
	}
	return normalized
}

func isDecimalDigits(value string) bool {
	for _, ch := range value {
		if ch < '0' || ch > '9' {
			return false
		}
	}
	return true
}
