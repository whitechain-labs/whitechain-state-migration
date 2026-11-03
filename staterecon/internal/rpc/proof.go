package rpc

import (
	"context"
	"strings"

	"github.com/whitechain-labs/whitechain-state-migration/staterecon/internal/models"
)

// FetchProofs calls eth_getProof for each address and returns normalized proof data.
func (c *Client) FetchProofs(ctx context.Context, addresses []string, blockTag string) []ProofResult {
	rawProofs := make([]proofRPCResponse, len(addresses))
	elems := make([]BatchElem, len(addresses))
	for i, addr := range addresses {
		elems[i] = BatchElem{
			Method: "eth_getProof",
			Params: []any{addr, []string{}, blockTag},
			Result: &rawProofs[i],
		}
	}
	_ = c.BatchCall(ctx, elems)

	out := make([]ProofResult, len(addresses))
	for i := range addresses {
		out[i] = ProofResult{Errors: make(map[string]string, 2)}

		if elems[i].Err != nil {
			msg := elems[i].Err.Error()
			out[i].Errors["code_hash"] = msg
			out[i].Errors["storage_hash"] = msg
			continue
		}

		codeHash, err := models.NormalizeCodeHash(rawProofs[i].CodeHash)
		switch {
		case err != nil:
			out[i].Errors["code_hash"] = err.Error()
		case codeHash == "":
			out[i].Errors["code_hash"] = "proof codeHash is empty"
		default:
			out[i].CodeHash = codeHash
		}

		storageHashRaw := strings.TrimSpace(rawProofs[i].StorageHash)
		if storageHashRaw == "" {
			storageHashRaw = strings.TrimSpace(rawProofs[i].StorageRoot)
		}
		if storageHashRaw == "" {
			out[i].Errors["storage_hash"] = "proof storageHash is empty"
			continue
		}

		storageHash, err := models.NormalizeWord32(storageHashRaw)
		if err != nil {
			out[i].Errors["storage_hash"] = err.Error()
		} else {
			out[i].StorageHash = storageHash
		}
	}
	return out
}
