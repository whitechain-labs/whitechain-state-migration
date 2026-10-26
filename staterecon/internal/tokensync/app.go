package tokensync

import (
	"context"
	"fmt"
	"strings"

	"github.com/ethereum/go-ethereum/common"
	log "github.com/sirupsen/logrus"

	"github.com/whitechain-labs/whitechain-state-migration/staterecon/internal/models"
)

// Run loads token addresses, fetches metadata from the legacy explorer, and syncs it into Blockscout.
func Run(ctx context.Context, opts *Options) (Summary, error) {
	if opts == nil {
		return Summary{}, fmt.Errorf("options are nil")
	}

	targets, explorer, db, err := prepareRun(ctx, opts)
	if err != nil {
		return Summary{}, err
	}
	defer closeStore(ctx, db)

	summary := Summary{
		Tokens: len(targets),
	}

	for _, target := range targets {
		if err := syncTarget(ctx, db, explorer, target, opts.Force, &summary); err != nil {
			return summary, err
		}
	}

	return summary, nil
}

func prepareRun(ctx context.Context, opts *Options) ([]tokenTarget, *explorerClient, *store, error) {
	cfg, err := LoadConfig(opts.ConfigPath)
	if err != nil {
		return nil, nil, nil, err
	}

	targets, err := BuildTargets(cfg)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("build token targets: %w", err)
	}

	explorer, err := newExplorerClient(opts.OldExplorerURL, opts.HTTPTimeout)
	if err != nil {
		return nil, nil, nil, err
	}

	db, err := newStore(ctx, opts.BlockscoutDSN, opts.SSH)
	if err != nil {
		return nil, nil, nil, err
	}

	return targets, explorer, db, nil
}

type storeCloser interface {
	Close(context.Context) error
}

func closeStore(ctx context.Context, closer storeCloser) {
	if closer == nil {
		return
	}
	if err := closer.Close(ctx); err != nil {
		log.WithError(err).Warn("close tokensync store")
	}
}

func syncTarget(
	ctx context.Context,
	db *store,
	explorer *explorerClient,
	target tokenTarget,
	force bool,
	summary *Summary,
) error {
	summary.addType(target.Type)

	existing, err := lookupExistingToken(ctx, db, target)
	if err != nil {
		return err
	}

	action, reason := resolveSyncAction(existing, force)
	if action == syncActionSkip {
		summary.Skipped++
		logSkippedTarget(target, force, existing, reason)
		return nil
	}

	record, err := fetchTokenRecord(ctx, explorer, target)
	if err != nil {
		return err
	}

	if err := applySyncAction(ctx, db, action, &record, summary); err != nil {
		return err
	}

	logSyncedTarget(target, &record, action)
	return nil
}

func lookupExistingToken(ctx context.Context, db *store, target tokenTarget) (*existingToken, error) {
	existing, err := db.LookupToken(ctx, common.HexToAddress(target.Address).Bytes())
	if err != nil {
		return nil, fmt.Errorf("lookup token %s: %w", target.Address, err)
	}
	return existing, nil
}

func fetchTokenRecord(ctx context.Context, explorer *explorerClient, target tokenTarget) (tokenRecord, error) {
	token, err := explorer.FetchToken(ctx, target.Address)
	if err != nil {
		return tokenRecord{}, fmt.Errorf("fetch token %s: %w", target.Address, err)
	}

	record, err := buildTokenRecord(target, token)
	if err != nil {
		return tokenRecord{}, fmt.Errorf("prepare token %s: %w", target.Address, err)
	}

	return record, nil
}

func applySyncAction(
	ctx context.Context,
	db *store,
	action syncAction,
	record *tokenRecord,
	summary *Summary,
) error {
	switch action {
	case syncActionInsert:
		if err := db.InsertToken(ctx, record); err != nil {
			return err
		}
		summary.Inserted++
		return nil
	case syncActionUpdate:
		if err := db.UpdateToken(ctx, record); err != nil {
			return err
		}
		summary.Updated++
		return nil
	default:
		return fmt.Errorf("unsupported sync action %q for token %s", action, record.Address)
	}
}

func logSkippedTarget(target tokenTarget, force bool, existing *existingToken, reason string) {
	entry := log.WithFields(log.Fields{
		"address": target.Address,
		"type":    target.Type,
		"section": target.Section,
		"force":   force,
	})
	if existing != nil {
		entry = entry.WithFields(log.Fields{
			"holder_count":   existing.HolderCount,
			"transfer_count": existing.TransferCount,
		})
	}

	if force && existing != nil && (existing.HolderCount > 0 || existing.TransferCount > 0) {
		entry.Warn(reason)
		return
	}

	entry.Info(reason)
}

func logSyncedTarget(target tokenTarget, record *tokenRecord, action syncAction) {
	log.WithFields(log.Fields{
		"address":      record.Address,
		"type":         record.Type,
		"section":      target.Section,
		"name":         record.Name,
		"symbol":       record.Symbol,
		"decimals":     record.Decimals,
		"total_supply": record.TotalSupply,
		"action":       action,
	}).Info("token synced")
}

func buildTokenRecord(target tokenTarget, token *explorerToken) (tokenRecord, error) {
	if token == nil {
		return tokenRecord{}, fmt.Errorf("token payload is nil")
	}
	if err := validateDecimals(token.Decimals); err != nil {
		return tokenRecord{}, fmt.Errorf("token %s returned invalid decimals: %w", target.Address, err)
	}

	if token.Contract != "" {
		normalizedContract, err := models.NormalizeAddress(token.Contract)
		if err != nil {
			return tokenRecord{}, fmt.Errorf("legacy explorer contract address: %w", err)
		}
		if normalizedContract != target.Address {
			return tokenRecord{}, fmt.Errorf("legacy explorer returned contract %s for configured token %s", normalizedContract, target.Address)
		}
	}

	totalSupply, err := convertTotalSupply(token.TotalSupply, token.Decimals)
	if err != nil {
		return tokenRecord{}, err
	}

	return tokenRecord{
		Address:         target.Address,
		Name:            strings.TrimSpace(token.Name),
		Symbol:          strings.TrimSpace(token.Symbol),
		TotalSupply:     totalSupply,
		Decimals:        token.Decimals,
		Type:            target.Type,
		ContractAddress: common.HexToAddress(target.Address).Bytes(),
	}, nil
}

func resolveSyncAction(existing *existingToken, force bool) (syncAction, string) {
	if existing == nil {
		return syncActionInsert, ""
	}
	if !force {
		return syncActionSkip, "token already added, skipping"
	}
	if existing.HolderCount > 0 || existing.TransferCount > 0 {
		return syncActionSkip, fmt.Sprintf(
			"token already has holder_count=%d and transfer_count=%d in Blockscout, skipping force update",
			existing.HolderCount,
			existing.TransferCount,
		)
	}
	return syncActionUpdate, ""
}
