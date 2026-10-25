package dumpcheck

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math/big"
	"strings"

	"github.com/ethereum/go-ethereum/common"
	"github.com/holiman/uint256"
)

// decodeHex decodes hex with or without a 0x prefix. An odd number of digits is left-padded.
func decodeHex(s string) ([]byte, error) {
	h := strings.TrimPrefix(strings.TrimPrefix(s, "0x"), "0X")
	if len(h)%2 == 1 {
		h = "0" + h
	}
	b, err := hex.DecodeString(h)
	if err != nil {
		return nil, fmt.Errorf("invalid hex %q", s)
	}
	return b, nil
}

// decodeWord decodes up to 32 bytes of hex into a left-padded hash.
func decodeWord(s string) (common.Hash, error) {
	b, err := decodeHex(s)
	if err != nil {
		return common.Hash{}, err
	}
	if len(b) > common.HashLength {
		return common.Hash{}, fmt.Errorf("value exceeds 32 bytes: %q", s)
	}
	return common.BytesToHash(b), nil
}

// parseAddress parses a 20-byte address with or without a 0x prefix.
func parseAddress(s string) (common.Address, error) {
	b, err := decodeHex(s)
	if err != nil || len(strings.TrimPrefix(strings.TrimPrefix(s, "0x"), "0X")) != 2*common.AddressLength {
		return common.Address{}, fmt.Errorf("invalid address %q", s)
	}
	return common.BytesToAddress(b), nil
}

// parseQuantity parses a 0x-prefixed hex or a decimal quantity, as a JSON string or number. An
// absent or null value is zero, as in a genesis file.
func parseQuantity(raw json.RawMessage) (*uint256.Int, error) {
	s := strings.TrimSpace(string(raw))
	if s == "" || s == "null" {
		return new(uint256.Int), nil
	}
	if strings.HasPrefix(s, `"`) {
		if err := json.Unmarshal(raw, &s); err != nil {
			return nil, fmt.Errorf("invalid quantity %s", raw)
		}
	}
	n := new(big.Int)
	var ok bool
	switch {
	case strings.HasPrefix(s, "0x") || strings.HasPrefix(s, "0X"):
		h := s[2:]
		if h == "" {
			h = "0"
		}
		_, ok = n.SetString(h, 16)
	default:
		_, ok = n.SetString(s, 10)
	}
	if !ok || n.Sign() < 0 {
		return nil, fmt.Errorf("invalid quantity %s", raw)
	}
	v, overflow := uint256.FromBig(n)
	if overflow {
		return nil, fmt.Errorf("quantity exceeds 256 bits: %s", raw)
	}
	return v, nil
}

func trimLeftZeros(b []byte) []byte {
	return bytes.TrimLeft(b, "\x00")
}
