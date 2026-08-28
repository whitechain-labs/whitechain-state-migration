package main

import (
	"fmt"
	"os"
	"time"

	log "github.com/sirupsen/logrus"
	"github.com/urfave/cli/v2"

	"github.com/whitechainio/whitechain-utils/staterecon/internal/cliutil"
	"github.com/whitechainio/whitechain-utils/staterecon/internal/tokenrecon"
)

func main() {
	cliApp := &cli.App{
		Name:           "tokenrecon",
		Usage:          "Reconcile ERC20/ERC721 token state between L1 and L2 chains",
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
				Name:    "config",
				EnvVars: []string{"TOKEN_CONFIG"},
				Usage:   "Path to token config YAML file",
				Value:   "config/config.yml",
			},
			&cli.StringFlag{
				Name:    "output-dir",
				EnvVars: []string{"OUTPUT_DIR"},
				Usage:   "Directory for output files (tokenrecon_report_full.csv, tokenrecon_report_mismatch.csv, tokenrecon_report_summary.txt)",
				Value:   ".",
			},
		},
		Action: runAction,
	}
	cliutil.Run(cliApp, os.Args, "tokenrecon failed")
}

func runAction(c *cli.Context) error {
	ctx, stop := cliutil.SignalContext()
	defer stop()
	const outputPrefix = "tokenrecon"
	opts := &tokenrecon.Options{
		L1RPC:        c.String("l1-rpc"),
		L2RPC:        c.String("l2-rpc"),
		L1Block:      c.String("l1-block"),
		L2Block:      c.String("l2-block"),
		RPCBatchSize: c.Int("rpc-batch-size"),
		RPCTimeout:   c.Duration("rpc-timeout"),
		ConfigPath:   c.String("config"),
		OutputDir:    c.String("output-dir"),
		OutputPrefix: outputPrefix,
	}
	summary, err := tokenrecon.Run(ctx, opts)
	if err != nil {
		return fmt.Errorf("token reconciliation failed: %w", err)
	}
	log.WithFields(log.Fields{
		"tokens":     summary.Tokens,
		"checks":     summary.Checks,
		"matched":    summary.Matched,
		"mismatched": summary.Mismatched,
		"errors":     summary.Errors,
	}).Info("token reconciliation complete")
	return nil
}
