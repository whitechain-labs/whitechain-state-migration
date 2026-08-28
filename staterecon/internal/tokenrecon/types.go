package tokenrecon

import "time"

// Options holds runtime parameters for token reconciliation.
type Options struct {
	L1RPC        string
	L2RPC        string
	L1Block      string
	L2Block      string
	RPCBatchSize int
	RPCTimeout   time.Duration
	ConfigPath   string
	OutputDir    string
	OutputPrefix string
}

// Summary holds aggregate results across all reconciled tokens.
type Summary struct {
	Tokens     int
	Checks     int
	Matched    int
	Mismatched int
	Errors     int
}

type tokenSummary struct {
	Checks     int
	Matched    int
	Mismatched int
	Errors     int
}

type callSpec struct {
	field    string
	calldata string
}

type callResult struct {
	data string
	err  error
}
