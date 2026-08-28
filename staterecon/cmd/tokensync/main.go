package main

import (
	"fmt"
	"os"
	"time"

	log "github.com/sirupsen/logrus"
	"github.com/urfave/cli/v2"

	"github.com/whitechainio/whitechain-utils/staterecon/internal/cliutil"
	"github.com/whitechainio/whitechain-utils/staterecon/internal/tokensync"
)

func main() {
	cliApp := &cli.App{
		Name:           "tokensync",
		Usage:          "Sync token metadata from a legacy explorer into the Blockscout tokens table",
		ExitErrHandler: func(_ *cli.Context, _ error) {},
		Flags: []cli.Flag{
			&cli.StringFlag{
				Name:     "old-explorer-url",
				EnvVars:  []string{"OLD_EXPLORER_URL"},
				Usage:    "Legacy explorer base URL used to fetch token metadata",
				Required: true,
			},
			&cli.StringFlag{
				Name:     "blockscout-dsn",
				EnvVars:  []string{"BLOCKSCOUT_POSTGRES_DSN", "BLOCKSCOUT_DSN"},
				Usage:    "PostgreSQL DSN for the Blockscout database",
				Required: true,
			},
			&cli.StringFlag{
				Name:    "config",
				EnvVars: []string{"TOKEN_SYNC_CONFIG", "TOKEN_CONFIG"},
				Usage:   "Path to token config YAML file",
				Value:   "config/token-config.yml",
			},
			&cli.DurationFlag{
				Name:    "http-timeout",
				EnvVars: []string{"HTTP_TIMEOUT"},
				Usage:   "Per-request timeout for legacy explorer API calls (e.g. 20s)",
				Value:   20 * time.Second,
			},
			&cli.BoolFlag{
				Name:    "force",
				EnvVars: []string{"FORCE"},
				Usage:   "Update existing tokens if they have no holders and no transfers",
			},
			&cli.StringFlag{
				Name:    "ssh-host",
				EnvVars: []string{"SSH_HOST"},
				Usage:   "SSH bastion host for Blockscout Postgres access",
			},
			&cli.IntFlag{
				Name:    "ssh-port",
				EnvVars: []string{"SSH_PORT"},
				Usage:   "SSH bastion port for Blockscout Postgres access",
				Value:   22,
			},
			&cli.StringFlag{
				Name:    "ssh-user",
				EnvVars: []string{"SSH_USER"},
				Usage:   "SSH username for Blockscout Postgres access",
			},
			&cli.StringFlag{
				Name:    "ssh-key-file",
				EnvVars: []string{"SSH_KEY_FILE"},
				Usage:   "Path to SSH private key for Blockscout Postgres access",
			},
			&cli.StringFlag{
				Name:    "ssh-trust-store",
				EnvVars: []string{"SSH_TRUST_STORE"},
				Usage:   "Path to tokensync SSH trust store file for TOFU host key pinning",
				Value:   tokensync.DefaultSSHTrustStorePath(),
			},
		},
		Action: runAction,
	}

	cliutil.Run(cliApp, os.Args, "tokensync failed")
}

func runAction(c *cli.Context) error {
	ctx, stop := cliutil.SignalContext()
	defer stop()

	opts := &tokensync.Options{
		OldExplorerURL: c.String("old-explorer-url"),
		BlockscoutDSN:  c.String("blockscout-dsn"),
		ConfigPath:     c.String("config"),
		HTTPTimeout:    c.Duration("http-timeout"),
		Force:          c.Bool("force"),
		SSH: tokensync.SSHOptions{
			Host:           c.String("ssh-host"),
			Port:           c.Int("ssh-port"),
			User:           c.String("ssh-user"),
			PrivateKeyPath: c.String("ssh-key-file"),
			TrustStorePath: c.String("ssh-trust-store"),
		},
	}

	summary, err := tokensync.Run(ctx, opts)
	if err != nil {
		return fmt.Errorf("token sync failed: %w", err)
	}

	log.WithFields(log.Fields{
		"tokens":   summary.Tokens,
		"types":    summary.TypeCounts,
		"inserted": summary.Inserted,
		"updated":  summary.Updated,
		"skipped":  summary.Skipped,
	}).Info("token sync complete")

	return nil
}
