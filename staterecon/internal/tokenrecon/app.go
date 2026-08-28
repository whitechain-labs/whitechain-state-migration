package tokenrecon

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	log "github.com/sirupsen/logrus"

	"github.com/whitechainio/whitechain-utils/staterecon/internal/models"
	"github.com/whitechainio/whitechain-utils/staterecon/internal/output"
	"github.com/whitechainio/whitechain-utils/staterecon/internal/rpc"
)

type tokenEntry struct {
	name string
	kind string
	s    tokenSummary
}

// Run loads the token config, reconciles all tokens against L1 and L2, writes reports, and returns a summary.
func Run(ctx context.Context, opts *Options) (Summary, error) {
	generatedAt := time.Now()
	cfg, err := LoadConfig(opts.ConfigPath)
	if err != nil {
		return Summary{}, err
	}
	reporter, err := output.NewReporter(opts.OutputDir, opts.OutputPrefix)
	if err != nil {
		return Summary{}, fmt.Errorf("initialize reporter: %w", err)
	}
	defer reporter.Close()
	l1 := rpc.NewClient(opts.L1RPC, opts.RPCTimeout, opts.RPCBatchSize)
	l2 := rpc.NewClient(opts.L2RPC, opts.RPCTimeout, opts.RPCBatchSize)
	total := Summary{}
	entries := make([]tokenEntry, 0, len(cfg.ERC20)+len(cfg.ERC721))
	for i := range cfg.ERC20 {
		token := &cfg.ERC20[i]
		rows, s := reconcileToken(ctx, l1, l2, token, opts.L1Block, opts.L2Block, buildERC20Specs)
		if err := reporter.WriteRows(rows); err != nil {
			return total, fmt.Errorf("write rows for %s: %w", token.Name, err)
		}
		mergeInto(&total, token.Name, "erc20", s)
		entries = append(entries, tokenEntry{token.Name, "erc20", s})
	}
	for i := range cfg.ERC721 {
		token := &cfg.ERC721[i]
		rows, s := reconcileToken(ctx, l1, l2, token, opts.L1Block, opts.L2Block, buildERC721Specs)
		if err := reporter.WriteRows(rows); err != nil {
			return total, fmt.Errorf("write rows for %s: %w", token.Name, err)
		}
		mergeInto(&total, token.Name, "erc721", s)
		entries = append(entries, tokenEntry{token.Name, "erc721", s})
	}
	if err := writeTokenSummary(opts, generatedAt, total, entries); err != nil {
		return total, fmt.Errorf("write summary: %w", err)
	}
	return total, nil
}

func mergeInto(total *Summary, name, kind string, s tokenSummary) {
	total.Tokens++
	total.Checks += s.Checks
	total.Matched += s.Matched
	total.Mismatched += s.Mismatched
	total.Errors += s.Errors
	log.WithFields(log.Fields{
		"token":      name,
		"type":       kind,
		"checks":     s.Checks,
		"matched":    s.Matched,
		"mismatched": s.Mismatched,
		"errors":     s.Errors,
	}).Info("token reconciled")
}

func writeTokenSummary(opts *Options, generatedAt time.Time, total Summary, entries []tokenEntry) error {
	path := filepath.Join(opts.OutputDir, opts.OutputPrefix+"_report_summary.txt")
	f, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("create summary file: %w", err)
	}
	defer f.Close()
	lines := []string{
		"=== Token Reconciliation Summary ===",
		fmt.Sprintf("Generated:             %s", generatedAt.Format(time.RFC3339)),
		"",
		"--- Configuration ---",
		fmt.Sprintf("L1 RPC:                %s", opts.L1RPC),
		fmt.Sprintf("L2 RPC:                %s", opts.L2RPC),
		fmt.Sprintf("L1 Block:              %s", opts.L1Block),
		fmt.Sprintf("L2 Block:              %s", opts.L2Block),
		"",
		"--- Results ---",
		fmt.Sprintf("Tokens:                %d", total.Tokens),
		fmt.Sprintf("Checks total:          %d", total.Checks),
		fmt.Sprintf("  Matched:             %d", total.Matched),
		fmt.Sprintf("  Mismatched:          %d", total.Mismatched),
		fmt.Sprintf("  Errors:              %d", total.Errors),
		"",
		"--- Mismatches by token ---",
	}
	for _, e := range entries {
		lines = append(lines, fmt.Sprintf(
			"%-24s (%s)  checks: %d  matched: %d  mismatched: %d  errors: %d",
			e.name, e.kind, e.s.Checks, e.s.Matched, e.s.Mismatched, e.s.Errors,
		))
	}
	lines = append(lines, "")
	for _, line := range lines {
		if _, err := fmt.Fprintln(f, line); err != nil {
			return fmt.Errorf("write summary: %w", err)
		}
	}
	return nil
}

func reconcileToken(
	ctx context.Context,
	l1, l2 *rpc.Client,
	token *TokenConfig,
	l1Block, l2Block string,
	buildSpecs func(*TokenConfig) []callSpec,
) ([]models.ReportRow, tokenSummary) {
	specs := buildSpecs(token)
	rows := make([]models.ReportRow, 0, len(specs)+len(token.Storages))
	rows = append(rows, fetchAndCompare(ctx, l1, l2, token.Address, specs, l1Block, l2Block)...)
	rows = append(rows, fetchAndCompareStorage(ctx, l1, l2, token.Address, token.Storages, l1Block, l2Block)...)
	rows = append(rows, fetchAndCompareProof(ctx, l1, l2, token.Address, token.Rules, l1Block, l2Block)...)
	s := tokenSummary{Checks: len(rows)}
	for _, row := range rows {
		countStatus(&s, row.Status)
	}
	return rows, s
}

func fetchAndCompareProof(ctx context.Context, l1Client, l2Client *rpc.Client, addr string, rules []string, l1Block, l2Block string) []models.ReportRow {
	wantCodeHash := hasRule(rules, "codehash")
	wantStorageHash := hasRule(rules, "storagehash")
	if !wantCodeHash && !wantStorageHash {
		return nil
	}

	var l1Proofs, l2Proofs []rpc.ProofResult
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); l1Proofs = l1Client.FetchProofs(ctx, []string{addr}, l1Block) }()
	go func() { defer wg.Done(); l2Proofs = l2Client.FetchProofs(ctx, []string{addr}, l2Block) }()
	wg.Wait()

	l1P := l1Proofs[0]
	l2P := l2Proofs[0]

	var rows []models.ReportRow
	if wantCodeHash {
		rows = append(rows, makeRow(addr, "code_hash",
			callResult{data: l1P.CodeHash, err: strToErr(l1P.Errors["code_hash"])},
			callResult{data: l2P.CodeHash, err: strToErr(l2P.Errors["code_hash"])},
		))
	}
	if wantStorageHash {
		rows = append(rows, makeRow(addr, "storage_hash",
			callResult{data: l1P.StorageHash, err: strToErr(l1P.Errors["storage_hash"])},
			callResult{data: l2P.StorageHash, err: strToErr(l2P.Errors["storage_hash"])},
		))
	}
	return rows
}

func hasRule(rules []string, rule string) bool {
	for _, r := range rules {
		if r == rule {
			return true
		}
	}
	return false
}

func strToErr(s string) error {
	if s == "" {
		return nil
	}
	return fmt.Errorf("%s", s)
}

func fetchAndCompare(ctx context.Context, l1, l2 *rpc.Client, addr string, specs []callSpec, l1Block, l2Block string) []models.ReportRow {
	if len(specs) == 0 {
		return nil
	}
	var l1R, l2R []callResult
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); l1R = batchEthCall(ctx, l1, addr, specs, l1Block) }()
	go func() { defer wg.Done(); l2R = batchEthCall(ctx, l2, addr, specs, l2Block) }()
	wg.Wait()
	rows := make([]models.ReportRow, len(specs))
	for i, spec := range specs {
		rows[i] = makeRow(addr, spec.field, l1R[i], l2R[i])
	}
	return rows
}

func fetchAndCompareStorage(ctx context.Context, l1, l2 *rpc.Client, addr string, slots []string, l1Block, l2Block string) []models.ReportRow {
	if len(slots) == 0 {
		return nil
	}
	var l1S, l2S []callResult
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); l1S = batchGetStorageAt(ctx, l1, addr, slots, l1Block) }()
	go func() { defer wg.Done(); l2S = batchGetStorageAt(ctx, l2, addr, slots, l2Block) }()
	wg.Wait()
	rows := make([]models.ReportRow, len(slots))
	for i, slot := range slots {
		rows[i] = makeRow(addr, "storage:"+slot, l1S[i], l2S[i])
	}
	return rows
}

func makeRow(address, field string, l1, l2 callResult) models.ReportRow {
	l1Err, l2Err := "", ""
	if l1.err != nil {
		l1Err = l1.err.Error()
	}
	if l2.err != nil {
		l2Err = l2.err.Error()
	}
	return models.MakeReportRow(address, field, l1.data, l2.data, "value mismatch", l1Err, l2Err)
}

func countStatus(s *tokenSummary, status models.Status) {
	switch status {
	case models.StatusError:
		s.Errors++
	case models.StatusMismatch:
		s.Mismatched++
	default:
		s.Matched++
	}
}
