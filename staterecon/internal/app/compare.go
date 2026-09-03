package app

import (
	"context"
	"strings"
	"sync"

	log "github.com/sirupsen/logrus"

	"github.com/whitechain-labs/whitechain-state-migration/staterecon/internal/models"
	"github.com/whitechain-labs/whitechain-state-migration/staterecon/internal/rpc"
)

func compareBatch(
	ctx context.Context,
	l1Client *rpc.Client,
	l2Client *rpc.Client,
	accounts []models.AccountSnapshot,
	opts *Options,
) (batchSummary, []models.ReportRow) {
	result := batchSummary{Accounts: len(accounts)}
	rows := make([]models.ReportRow, 0, len(accounts)*4)

	l2State := fetchChainBasics(ctx, l2Client, accounts, opts.L2Block)
	contractIndexes, contracts := collectContracts(accounts)
	l1Proofs, l2Proofs, proofByAccount := fetchContractProofs(ctx, l1Client, l2Client, contractIndexes, contracts, opts, &result)

	for index, account := range accounts {
		start := len(rows)

		var l1Proof, l2Proof *rpc.ProofResult
		if pi, ok := proofByAccount[index]; ok {
			l1Proof = &l1Proofs[pi]
			l2Proof = &l2Proofs[pi]
		}

		balanceRow := compareField(account.Address, "balance",
			account.Balance, l2State[index].Balance,
			"native balance mismatch", "", l2State[index].Errors["balance"])
		rows = append(rows, balanceRow)
		if balanceRow.Status == models.StatusMismatch {
			result.BalanceMismatches++
		}

		nonceRow := compareField(account.Address, "nonce",
			account.Nonce, l2State[index].Nonce,
			"nonce mismatch", "", l2State[index].Errors["nonce"])
		rows = append(rows, nonceRow)
		if nonceRow.Status == models.StatusMismatch {
			result.NonceMismatches++
		}

		if l1Proof != nil {
			codeHashRow := compareField(account.Address, "code_hash",
				l1Proof.CodeHash, l2Proof.CodeHash,
				"contract codehash mismatch", l1Proof.Errors["code_hash"], l2Proof.Errors["code_hash"])
			rows = append(rows, codeHashRow)
			if codeHashRow.Status == models.StatusMismatch {
				result.CodeHashMismatches++
			}

			storageHashRow := compareField(account.Address, "storage_hash",
				l1Proof.StorageHash, l2Proof.StorageHash,
				"contract storagehash mismatch", l1Proof.Errors["storage_hash"], l2Proof.Errors["storage_hash"])
			rows = append(rows, storageHashRow)
			if storageHashRow.Status == models.StatusMismatch {
				result.StorageHashMismatches++
			}
		}

		accountStatus := models.AggregateStatus(rows[start:])
		switch accountStatus {
		case models.StatusError:
			result.Errors++
		case models.StatusMismatch:
			result.Mismatched++
		default:
			result.Matched++
		}

		logAccountResult(account.Address, accountStatus, rows[start:], opts)
	}

	return result, rows
}

func fetchContractProofs(
	ctx context.Context,
	l1Client, l2Client *rpc.Client,
	contractIndexes []int,
	contracts []models.AccountSnapshot,
	opts *Options,
	result *batchSummary,
) (l1Proofs, l2Proofs []rpc.ProofResult, proofByAccount map[int]int) {
	proofByAccount = make(map[int]int, len(contractIndexes))
	for pi, ai := range contractIndexes {
		proofByAccount[ai] = pi
	}
	if len(contracts) == 0 {
		return nil, nil, proofByAccount
	}

	addresses := make([]string, len(contracts))
	for i, c := range contracts {
		addresses[i] = c.Address
	}

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		l1Proofs = l1Client.FetchProofs(ctx, addresses, opts.L1Block)
	}()
	go func() {
		defer wg.Done()
		l2Proofs = l2Client.FetchProofs(ctx, addresses, opts.L2Block)
	}()
	wg.Wait()

	result.ProofChecks = len(contracts) * 2
	result.ProofCheckAccounts = len(contracts)
	return l1Proofs, l2Proofs, proofByAccount
}

func logAccountResult(address string, status models.Status, rows []models.ReportRow, opts *Options) {
	if !opts.PrintComparisons && !(opts.PrintMismatches && status == models.StatusMismatch) {
		return
	}
	log.WithFields(log.Fields{
		"address": address,
		"status":  status,
	}).Info("account")
	for _, row := range rows {
		if row.Status == models.StatusMatch {
			continue
		}
		log.WithFields(log.Fields{
			"address": address,
			"field":   row.Field,
			"reason":  row.Error,
			"l1":      displayValue(row.L1Value),
			"l2":      displayValue(row.L2Value),
		}).Info("  field mismatch")
	}
}

func collectContracts(accounts []models.AccountSnapshot) (indexes []int, contracts []models.AccountSnapshot) {
	for index, account := range accounts {
		hasCode := account.Type == models.AccountTypeContract
		if account.Code != nil {
			hasCode = !models.IsZeroCode(*account.Code)
		}
		if hasCode && !isZeroOrEmptyCodeHash(account.CodeHash) {
			indexes = append(indexes, index)
			contracts = append(contracts, account)
		}
	}
	return indexes, contracts
}

func compareField(address, field, l1Value, l2Value, mismatchReason, l1Err, l2Err string) models.ReportRow {
	return models.MakeReportRow(address, field, l1Value, l2Value, mismatchReason, l1Err, l2Err)
}

// displayValue returns v as-is for short values (hashes, numbers).
// Hex blobs longer than a 32-byte hash (0x + 64 chars) are masked as <bytecode>.
func displayValue(v string) string {
	if strings.HasPrefix(v, "0x") && len(v) > 66 {
		return "<bytecode>"
	}
	return v
}

func isZeroOrEmptyCodeHash(value string) bool {
	normalized, err := models.NormalizeCodeHash(value)
	if err != nil || normalized == "" {
		return true
	}
	switch normalized {
	case models.ZeroHash(), models.EmptyCodeHash():
		return true
	default:
		return false
	}
}
