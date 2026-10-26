package tokenrecon

import (
	"context"
	"fmt"
	"strings"

	"github.com/ethereum/go-ethereum/crypto"

	"github.com/whitechain-labs/whitechain-state-migration/staterecon/internal/models"
	"github.com/whitechain-labs/whitechain-state-migration/staterecon/internal/rpc"
)

type ethCallMsg struct {
	To   string `json:"to"`
	Data string `json:"data"`
}

// fnCalldata computes hex calldata (4-byte selector) for a no-argument function signature.
func fnCalldata(sig string) string {
	h := crypto.Keccak256([]byte(sig))
	return "0x" + fmt.Sprintf("%x", h[:4])
}

// fnCalldataAddr computes hex calldata for a function with a single address argument.
func fnCalldataAddr(sig, addr string) (string, error) {
	normalized, err := models.NormalizeAddress(addr)
	if err != nil {
		return "", fmt.Errorf("invalid address %q: %w", addr, err)
	}
	trimmed := strings.TrimPrefix(normalized, "0x")
	h := crypto.Keccak256([]byte(sig))
	// ABI encoding: 4-byte selector + 32-byte address (left-padded with 12 zero bytes)
	return "0x" + fmt.Sprintf("%x", h[:4]) + strings.Repeat("0", 24) + trimmed, nil
}

// batchEthCall issues eth_call for each spec against a contract and returns raw hex return data.
func batchEthCall(ctx context.Context, client *rpc.Client, contractAddr string, specs []callSpec, blockTag string) []callResult {
	if len(specs) == 0 {
		return nil
	}
	results := make([]string, len(specs))
	elems := make([]rpc.BatchElem, len(specs))
	for i, spec := range specs {
		elems[i] = rpc.BatchElem{
			Method: "eth_call",
			Params: []any{ethCallMsg{To: contractAddr, Data: spec.calldata}, blockTag},
			Result: &results[i],
		}
	}
	_ = client.BatchCall(ctx, elems)
	out := make([]callResult, len(specs))
	for i := range specs {
		if elems[i].Err != nil {
			out[i] = callResult{err: elems[i].Err}
		} else {
			out[i] = callResult{data: strings.ToLower(strings.TrimSpace(results[i]))}
		}
	}
	return out
}

// batchGetStorageAt issues eth_getStorageAt for each slot and returns raw hex storage values.
func batchGetStorageAt(ctx context.Context, client *rpc.Client, contractAddr string, slots []string, blockTag string) []callResult {
	if len(slots) == 0 {
		return nil
	}
	results := make([]string, len(slots))
	elems := make([]rpc.BatchElem, len(slots))
	for i, slot := range slots {
		elems[i] = rpc.BatchElem{
			Method: "eth_getStorageAt",
			Params: []any{contractAddr, slot, blockTag},
			Result: &results[i],
		}
	}
	_ = client.BatchCall(ctx, elems)
	out := make([]callResult, len(slots))
	for i := range slots {
		if elems[i].Err != nil {
			out[i] = callResult{err: elems[i].Err}
		} else {
			out[i] = callResult{data: strings.ToLower(strings.TrimSpace(results[i]))}
		}
	}
	return out
}
