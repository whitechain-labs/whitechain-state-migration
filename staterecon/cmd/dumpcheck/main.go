package main

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	log "github.com/sirupsen/logrus"
	"github.com/urfave/cli/v2"

	"github.com/whitechain-labs/whitechain-state-migration/staterecon/internal/cliutil"
	"github.com/whitechain-labs/whitechain-state-migration/staterecon/internal/dumpcheck"
)

func main() {
	cliApp := &cli.App{
		Name:           "dumpcheck",
		Usage:          "Verify a geth state dump against its state root, and the allocation built from it",
		ExitErrHandler: func(_ *cli.Context, _ error) {},
		Flags: []cli.Flag{
			&cli.StringFlag{
				Name:     "dump",
				EnvVars:  []string{"DUMP"},
				Usage:    "geth JSONL state dump (state_dump_<BLOCK>.json)",
				Required: true,
			},
			&cli.StringFlag{
				Name:    "expected-root",
				EnvVars: []string{"EXPECTED_ROOT"},
				Usage:   "stateRoot of the snapshot block, as recorded at the freeze; the dump header must equal it",
			},
			&cli.StringFlag{
				Name:    "genesis",
				EnvVars: []string{"GENESIS"},
				Usage:   "base L2 genesis passed to dump-to-alloc.mjs; with --alloc, also verifies the allocation",
			},
			&cli.StringFlag{
				Name:    "alloc",
				EnvVars: []string{"ALLOC"},
				Usage:   "allocation written by dump-to-alloc.mjs (alloc.json); requires --genesis",
			},
		},
		Action: runAction,
	}

	cliutil.Run(cliApp, os.Args, "dumpcheck failed")
}

func runAction(c *cli.Context) error {
	genesisPath, allocPath := c.String("genesis"), c.String("alloc")
	if (genesisPath == "") != (allocPath == "") {
		return errors.New("--genesis and --alloc go together")
	}

	var expected *common.Hash
	if s := c.String("expected-root"); s != "" {
		b, err := hexutil.Decode(s)
		if err != nil || len(b) != common.HashLength {
			return fmt.Errorf("invalid --expected-root %q: want a 0x-prefixed 32-byte hash", s)
		}
		h := common.BytesToHash(b)
		expected = &h
	}

	dump, err := dumpcheck.VerifyDump(c.String("dump"), expected, allocPath != "")
	if dump != nil {
		log.WithFields(log.Fields{
			"header_root":           dump.HeaderRoot.Hex(),
			"recomputed_root":       dump.RecomputedRoot.Hex(),
			"accounts":              dump.Accounts,
			"contracts":             dump.Contracts,
			"with_storage":          dump.WithStorage,
			"empty":                 dump.Empty,
			"zero_address_restored": dump.ZeroAddressRestored,
			"balance_sum_wei":       dump.BalanceSum.String(),
		}).Info("dump checked")
	}
	if err != nil {
		return failed(err)
	}
	if expected == nil {
		log.Warn("no --expected-root: the dump is internally consistent, but not tied to the snapshot block")
	}
	if allocPath == "" {
		log.Info("dump verified")
		return nil
	}

	alloc, err := dumpcheck.VerifyAlloc(dump.ByAddress, dump.BalanceSum, genesisPath, allocPath)
	if alloc != nil {
		log.WithFields(log.Fields{
			"entries":               alloc.Entries,
			"from_dump":             alloc.FromDump,
			"protected_merged":      alloc.ProtectedMerged,
			"kept_over_empty":       alloc.KeptOverEmpty,
			"base_carried":          alloc.Carried,
			"pruned_empty":          alloc.Pruned,
			"alloc_balance_sum_wei": alloc.AllocBalanceSum.String(),
			"dump_balance_sum_wei":  alloc.DumpBalanceSum.String(),
			"base_retained_sum_wei": alloc.BaseRetainedSum.String(),
		}).Info("allocation checked")
	}
	if err != nil {
		return failed(err)
	}
	log.Info("dump and allocation verified")
	return nil
}

// failed prints the full problem list to stderr, one per line, and returns its first line: a
// logged error would print the list on a single line with escaped newlines.
func failed(err error) error {
	msg := err.Error()
	fmt.Fprintln(os.Stderr, msg)
	first, _, _ := strings.Cut(msg, "\n")
	return errors.New(first)
}
