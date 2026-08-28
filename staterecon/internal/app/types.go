package app

import "time"

type Options struct {
	L1RPC             string
	L2RPC             string
	L1Block           string
	L2Block           string
	PageSize          int
	BatchSize         int
	RPCBatchSize      int
	RPCTimeout        time.Duration
	AccountRangeStart string
	OutputDir         string
	OutputPrefix      string
	PrintMismatches   bool
	PrintComparisons  bool
}

type Summary struct {
	Pages                 int
	Batches               int
	Accounts              int
	Matched               int
	Mismatched            int
	Errors                int
	BalanceMismatches     int
	NonceMismatches       int
	CodeHashMismatches    int
	StorageHashMismatches int
	ProofChecks           int
	ProofCheckAccounts    int
}

type batchSummary struct {
	Accounts              int
	Matched               int
	Mismatched            int
	Errors                int
	BalanceMismatches     int
	NonceMismatches       int
	CodeHashMismatches    int
	StorageHashMismatches int
	ProofChecks           int
	ProofCheckAccounts    int
}

type chainBasicState struct {
	Balance string
	Nonce   string
	Errors  map[string]string
}
