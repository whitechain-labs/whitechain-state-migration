package tokensync

import (
	"context"
	"fmt"
	"net/url"
	"strings"

	"github.com/jackc/pgx/v5"
)

const insertTokenSQL = `
INSERT INTO tokens (
    name,
    symbol,
    total_supply,
    decimals,
    type,
    cataloged,
    contract_address_hash,
    inserted_at,
    updated_at,
    holder_count,
    transfer_count
) VALUES (
    $1,
    $2,
    CAST($3 AS numeric),
    CAST($4 AS numeric),
    $5,
    TRUE,
    $6,
    NOW(),
    NOW(),
    0,
    0
)`

const updateTokenSQL = `
UPDATE tokens
SET
    name = $1,
    symbol = $2,
    total_supply = CAST($3 AS numeric),
    decimals = CAST($4 AS numeric),
    type = $5,
    cataloged = TRUE,
    updated_at = NOW(),
    holder_count = 0,
    transfer_count = 0
WHERE contract_address_hash = $6`

const selectTokenSQL = `
SELECT
    COALESCE(holder_count, 0),
    COALESCE(transfer_count, 0)
FROM tokens
WHERE contract_address_hash = $1`

type store struct {
	conn      *pgx.Conn
	sshTunnel *sshTunnel
}

func newStore(ctx context.Context, dsn string, sshOptions SSHOptions) (*store, error) {
	normalizedDSN, err := normalizePostgresDSN(dsn)
	if err != nil {
		return nil, err
	}
	if normalizedDSN == "" {
		return nil, fmt.Errorf("blockscout dsn is empty")
	}

	connConfig, err := pgx.ParseConfig(normalizedDSN)
	if err != nil {
		return nil, fmt.Errorf("parse blockscout dsn: %w", err)
	}

	var tunnel *sshTunnel
	if sshOptions.Enabled() {
		tunnel, err = newSSHTunnel(ctx, sshOptions)
		if err != nil {
			return nil, err
		}
		connConfig.DialFunc = tunnel.DialContext
	}

	conn, err := pgx.ConnectConfig(ctx, connConfig)
	if err != nil {
		if tunnel != nil {
			_ = tunnel.Close()
		}
		return nil, fmt.Errorf("connect to blockscout postgres: %w", err)
	}

	return &store{
		conn:      conn,
		sshTunnel: tunnel,
	}, nil
}

func (s *store) Close(ctx context.Context) error {
	if s == nil {
		return nil
	}

	var firstErr error
	if s.conn != nil {
		if err := s.conn.Close(ctx); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	if s.sshTunnel != nil {
		if err := s.sshTunnel.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

func (s *store) LookupToken(ctx context.Context, contractAddress []byte) (*existingToken, error) {
	if s == nil || s.conn == nil {
		return nil, fmt.Errorf("store is not initialized")
	}

	var token existingToken
	if err := s.conn.QueryRow(ctx, selectTokenSQL, contractAddress).Scan(&token.HolderCount, &token.TransferCount); err != nil {
		if err == pgx.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("lookup token: %w", err)
	}

	return &token, nil
}

func (s *store) InsertToken(ctx context.Context, record *tokenRecord) error {
	if s == nil || s.conn == nil {
		return fmt.Errorf("store is not initialized")
	}
	if record == nil {
		return fmt.Errorf("token record is nil")
	}

	if _, err := s.conn.Exec(
		ctx,
		insertTokenSQL,
		record.Name,
		record.Symbol,
		record.TotalSupply,
		record.Decimals,
		string(record.Type),
		record.ContractAddress,
	); err != nil {
		return fmt.Errorf("insert token %s: %w", record.Address, err)
	}

	return nil
}

func normalizePostgresDSN(raw string) (string, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "", nil
	}

	if !strings.Contains(trimmed, "://") {
		return trimmed, nil
	}

	parsedURL, err := url.Parse(trimmed)
	if err != nil {
		return "", fmt.Errorf("parse blockscout dsn url: %w", err)
	}

	query := parsedURL.Query()
	if query.Get("sslmode") != "" {
		return trimmed, nil
	}

	sslValue := strings.TrimSpace(strings.ToLower(query.Get("ssl")))
	if sslValue == "" {
		return trimmed, nil
	}

	switch sslValue {
	case "false", "0", "off", "disable", "disabled", "no":
		query.Del("ssl")
		query.Set("sslmode", "disable")
	case "true", "1", "on", "enable", "enabled", "require", "required", "yes":
		query.Del("ssl")
		query.Set("sslmode", "require")
	default:
		return trimmed, nil
	}

	parsedURL.RawQuery = query.Encode()
	return parsedURL.String(), nil
}

func (s *store) UpdateToken(ctx context.Context, record *tokenRecord) error {
	if s == nil || s.conn == nil {
		return fmt.Errorf("store is not initialized")
	}
	if record == nil {
		return fmt.Errorf("token record is nil")
	}

	if _, err := s.conn.Exec(
		ctx,
		updateTokenSQL,
		record.Name,
		record.Symbol,
		record.TotalSupply,
		record.Decimals,
		string(record.Type),
		record.ContractAddress,
	); err != nil {
		return fmt.Errorf("update token %s: %w", record.Address, err)
	}

	return nil
}
