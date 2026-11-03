package app

import (
	"context"
	"fmt"
	"strings"
	"time"

	log "github.com/sirupsen/logrus"

	"github.com/whitechain-labs/whitechain-state-migration/staterecon/internal/models"
	"github.com/whitechain-labs/whitechain-state-migration/staterecon/internal/output"
	"github.com/whitechain-labs/whitechain-state-migration/staterecon/internal/rpc"
)

func (s *Summary) merge(other *batchSummary) {
	s.Batches++
	s.Accounts += other.Accounts
	s.Matched += other.Matched
	s.Mismatched += other.Mismatched
	s.Errors += other.Errors
	s.BalanceMismatches += other.BalanceMismatches
	s.NonceMismatches += other.NonceMismatches
	s.CodeHashMismatches += other.CodeHashMismatches
	s.StorageHashMismatches += other.StorageHashMismatches
	s.ProofChecks += other.ProofChecks
	s.ProofCheckAccounts += other.ProofCheckAccounts
}

func Run(ctx context.Context, options *Options) (Summary, error) {
	reporter, err := output.NewReporter(options.OutputDir, options.OutputPrefix)
	if err != nil {
		return Summary{}, fmt.Errorf("initialize reporter: %w", err)
	}
	defer reporter.Close()

	total, err := runReconciliation(ctx, options, reporter)
	if err != nil {
		return total, err
	}

	if err := reporter.WriteSummary(&output.ReportSummary{
		GeneratedAt:           time.Now(),
		L1RPC:                 options.L1RPC,
		L2RPC:                 options.L2RPC,
		L1Block:               options.L1Block,
		L2Block:               options.L2Block,
		Pages:                 total.Pages,
		Batches:               total.Batches,
		Accounts:              total.Accounts,
		Matched:               total.Matched,
		Mismatched:            total.Mismatched,
		Errors:                total.Errors,
		BalanceMismatches:     total.BalanceMismatches,
		NonceMismatches:       total.NonceMismatches,
		CodeHashMismatches:    total.CodeHashMismatches,
		StorageHashMismatches: total.StorageHashMismatches,
		ProofChecks:           total.ProofChecks,
		ProofCheckAccounts:    total.ProofCheckAccounts,
	}); err != nil {
		return total, fmt.Errorf("write summary: %w", err)
	}

	return total, nil
}

func runReconciliation(ctx context.Context, opts *Options, reporter *output.Reporter) (Summary, error) {
	l1Client := rpc.NewClient(opts.L1RPC, opts.RPCTimeout, opts.RPCBatchSize)
	l2Client := rpc.NewClient(opts.L2RPC, opts.RPCTimeout, opts.RPCBatchSize)

	cursor := opts.AccountRangeStart
	batch := make([]models.AccountSnapshot, 0, opts.BatchSize)
	total := Summary{}

	for {
		page, err := l1Client.AccountRange(ctx, opts.L1Block, cursor, opts.PageSize)
		if err != nil {
			return total, fmt.Errorf("fetch account range page %d: %w", total.Pages+1, err)
		}
		total.Pages++

		for _, account := range page.Accounts {
			batch = append(batch, account)

			if len(batch) < opts.BatchSize {
				continue
			}

			if err := flushBatch(ctx, l1Client, l2Client, batch, opts, reporter, &total, cursorOrEnd(page.Next)); err != nil {
				return total, err
			}
			batch = batch[:0]
		}

		if isLastPage(page.Next, cursor) {
			break
		}
		cursor = page.Next
	}

	if len(batch) > 0 {
		if err := flushBatch(ctx, l1Client, l2Client, batch, opts, reporter, &total, "END"); err != nil {
			return total, err
		}
	}

	return total, nil
}

func flushBatch(
	ctx context.Context,
	l1Client, l2Client *rpc.Client,
	batch []models.AccountSnapshot,
	opts *Options,
	reporter *output.Reporter,
	total *Summary,
	next string,
) error {
	batchResult, rows := compareBatch(ctx, l1Client, l2Client, batch, opts)
	if err := reporter.WriteRows(rows); err != nil {
		return fmt.Errorf("write csv rows: %w", err)
	}
	total.merge(&batchResult)
	log.WithFields(log.Fields{
		"batch":      total.Batches,
		"accounts":   batchResult.Accounts,
		"matched":    batchResult.Matched,
		"mismatched": batchResult.Mismatched,
		"errors":     batchResult.Errors,
		"next":       next,
	}).Info("batch processed")
	return nil
}

func isLastPage(next, cursor string) bool {
	return next == "" || next == cursor
}

func cursorOrEnd(next string) string {
	if strings.TrimSpace(next) == "" {
		return "END"
	}
	return next
}
