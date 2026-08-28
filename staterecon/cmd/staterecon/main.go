package main

import (
	"fmt"
	"os"
	"time"

	log "github.com/sirupsen/logrus"
	"github.com/urfave/cli/v2"

	"github.com/whitechainio/whitechain-utils/staterecon/internal/app"
	"github.com/whitechainio/whitechain-utils/staterecon/internal/cliutil"
)

func main() {
	cliApp := &cli.App{
		Name:           "staterecon",
		Usage:          "Reconcile state between L1 and L2 chains",
		ExitErrHandler: func(_ *cli.Context, _ error) {},
		Flags: []cli.Flag{
			&cli.StringFlag{
				Name:     "l1-rpc",
				EnvVars:  []string{"L1_RPC"},
				Usage:    "L1 JSON-RPC endpoint URL",
				Required: true,
			},
			&cli.StringFlag{
				Name:     "l2-rpc",
				EnvVars:  []string{"L2_RPC"},
				Usage:    "L2 JSON-RPC endpoint URL",
				Required: true,
			},
			&cli.StringFlag{
				Name:    "l1-block",
				EnvVars: []string{"L1_BLOCK"},
				Usage:   "L1 block tag or number for checks",
				Value:   "latest",
			},
			&cli.StringFlag{
				Name:    "l2-block",
				EnvVars: []string{"L2_BLOCK"},
				Usage:   "L2 block tag or number for checks",
				Value:   "latest",
			},
			&cli.IntFlag{
				Name:    "page-size",
				EnvVars: []string{"PAGE_SIZE"},
				Usage:   "debug_accountRange page size",
				Value:   1000,
			},
			&cli.IntFlag{
				Name:    "batch-size",
				EnvVars: []string{"BATCH_SIZE"},
				Usage:   "Accounts per reconciliation batch",
				Value:   250,
			},
			&cli.IntFlag{
				Name:    "rpc-batch-size",
				EnvVars: []string{"RPC_BATCH_SIZE"},
				Usage:   "JSON-RPC requests per batch call",
				Value:   100,
			},
			&cli.DurationFlag{
				Name:    "rpc-timeout",
				EnvVars: []string{"RPC_TIMEOUT"},
				Usage:   "Per-request RPC timeout (e.g. 20s)",
				Value:   20 * time.Second,
			},
			&cli.StringFlag{
				Name:    "account-range-start",
				EnvVars: []string{"ACCOUNT_RANGE_START"},
				Usage:   "Optional debug_accountRange start key (32-byte hex)",
			},
			&cli.StringFlag{
				Name:    "output-dir",
				EnvVars: []string{"OUTPUT_DIR"},
				Usage:   "Directory for output files (staterecon_report_full.csv, staterecon_report_mismatch.csv, staterecon_report_summary.txt)",
				Value:   ".",
			},
			&cli.BoolFlag{
				Name:    "print-mismatches",
				EnvVars: []string{"PRINT_MISMATCHES"},
				Usage:   "Print each mismatched account",
			},
			&cli.BoolFlag{
				Name:    "print-comparisons",
				EnvVars: []string{"PRINT_COMPARISONS"},
				Usage:   "Print per-account comparison status",
			},
		},
		Action: runAction,
	}

	cliutil.Run(cliApp, os.Args, "staterecon failed")
}

func runAction(c *cli.Context) error {
	ctx, stop := cliutil.SignalContext()
	defer stop()
	const outputPrefix = "staterecon"

	options := app.Options{
		L1RPC:             c.String("l1-rpc"),
		L2RPC:             c.String("l2-rpc"),
		L1Block:           c.String("l1-block"),
		L2Block:           c.String("l2-block"),
		PageSize:          c.Int("page-size"),
		BatchSize:         c.Int("batch-size"),
		RPCBatchSize:      c.Int("rpc-batch-size"),
		RPCTimeout:        c.Duration("rpc-timeout"),
		AccountRangeStart: c.String("account-range-start"),
		OutputDir:         c.String("output-dir"),
		OutputPrefix:      outputPrefix,
		PrintMismatches:   c.Bool("print-mismatches"),
		PrintComparisons:  c.Bool("print-comparisons"),
	}

	summary, err := app.Run(ctx, &options)
	if err != nil {
		return fmt.Errorf("reconciliation failed: %w", err)
	}

	log.WithFields(log.Fields{
		"pages":                  summary.Pages,
		"batches":                summary.Batches,
		"accounts_total":         summary.Accounts,
		"matched":                summary.Matched,
		"mismatched":             summary.Mismatched,
		"errors":                 summary.Errors,
		"balance_mismatches":     summary.BalanceMismatches,
		"nonce_mismatches":       summary.NonceMismatches,
		"codehash_mismatches":    summary.CodeHashMismatches,
		"storagehash_mismatches": summary.StorageHashMismatches,
		"proof_checks":           summary.ProofChecks,
		"proof_check_accounts":   summary.ProofCheckAccounts,
	}).Info("reconciliation complete")

	return nil
}
