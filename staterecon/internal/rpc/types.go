package rpc

import (
	"encoding/json"

	gethrpc "github.com/ethereum/go-ethereum/rpc"

	"github.com/whitechainio/whitechain-utils/staterecon/internal/models"
)

type AccountRangePage struct {
	Accounts []models.AccountSnapshot
	Next     string
}

type accountRangeEntry struct {
	Address  string          `json:"address"`
	Balance  json.RawMessage `json:"balance"`
	Nonce    json.RawMessage `json:"nonce"`
	CodeHash string          `json:"codeHash"`
	Code     *string         `json:"code"`
}

type BatchElem struct {
	Method string
	Params any
	Result any
	Err    error
}

type Client struct {
	url       string
	rpc       *gethrpc.Client
	batchSize int
	initErr   error
}

// ProofResult holds the normalized output of an eth_getProof call.
type ProofResult struct {
	CodeHash    string
	StorageHash string
	Errors      map[string]string
}

type proofRPCResponse struct {
	CodeHash    string `json:"codeHash"`
	StorageHash string `json:"storageHash"`
	StorageRoot string `json:"storageRoot"`
}
