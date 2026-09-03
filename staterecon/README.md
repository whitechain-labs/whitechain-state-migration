# StateRecon

`staterecon` compares account state between L1 and L2 and writes a CSV report with `match` / `mismatch` / `error` per field.

## Reconciliation Algorithm

1. Reads accounts from L1 using `debug_accountRange` page-by-page.
2. Processes accounts in chunks and writes CSV rows immediately (streaming, no full-state buffering).
3. For every account compares:
   - native `balance` (L1 `debug_accountRange` vs L2 `eth_getBalance`)
   - `nonce` (L1 `debug_accountRange` vs L2 `eth_getTransactionCount`)
4. If account is a contract (`code != 0x` and code hash is not empty/zero), requests `eth_getProof` on both L1 and L2 and compares:
   - `code_hash`
   - `storage_hash`
5. For every mismatch, stores reason in CSV `error` column.

## Build

```bash
cd staterecon
go build -o staterecon ./cmd/staterecon
go build -o tokenrecon ./cmd/tokenrecon
go build -o tokensync ./cmd/tokensync
```

## staterecon — Account State Reconciliation

Compares balance, nonce, code hash and storage hash for every account found via `debug_accountRange`.

```bash
./staterecon \
  --l1-rpc http://localhost:8545 \
  --l2-rpc http://localhost:9545
```

### Optional Flags

- `--l1-block` L1 block/tag (default: `latest`)
- `--l2-block` L2 block/tag (default: `latest`)
- `--page-size` `debug_accountRange` page size (default: `1000`)
- `--batch-size` accounts per reconciliation batch (default: `250`)
- `--rpc-batch-size` JSON-RPC requests per batch call (default: `100`)
- `--rpc-timeout` per-request timeout (default: `20s`)
- `--account-range-start` start cursor for `debug_accountRange`
- `--output-dir` directory for output files (default: `.`)
- `--print-mismatches` print mismatched account addresses
- `--print-comparisons` print account-level statuses

## tokenrecon — Token State Reconciliation

Reconciles ERC20 and ERC721 token contracts listed in a config YAML file. For each token it calls
view functions on both chains and compares the raw return data. Storage slots are checked via
`eth_getStorageAt`.

```bash
./tokenrecon \
  --l1-rpc http://localhost:8545 \
  --l2-rpc http://localhost:9545 \
  --config config/config.yml \
  --output-dir ./token_output
```

### Optional Flags

- `--l1-block` L1 block/tag (default: `latest`)
- `--l2-block` L2 block/tag (default: `latest`)
- `--rpc-batch-size` JSON-RPC requests per batch call (default: `100`)
- `--rpc-timeout` per-request timeout (default: `20s`)
- `--config` path to token config YAML (default: `config/config.yml`, env: `TOKEN_CONFIG`)
- `--output-dir` directory for output files (default: `.`)

### Token Config

`config/config.yml` lists the contracts and rules to check:

```yaml
config:
  erc20:
    - name: USDC
      address: 0xF97B9Bf62916f1EB42Dd906a7254603e7b9FC4a7
      addresses:         # addresses for balance/nonces checks
        - 0xabc...
      storages:          # storage slots to compare via eth_getStorageAt
        - 0x0
      rules:
        - general        # CANCEL_AUTHORIZATION_TYPEHASH, DOMAIN_SEPARATOR, PERMIT_TYPEHASH,
                         # RECEIVE_WITH_AUTHORIZATION_TYPEHASH, TRANSFER_WITH_AUTHORIZATION_TYPEHASH,
                         # currency, decimals, name, symbol, totalSupply, version
        - balance        # balanceOf(address), nonces(address) for each address above
        - roles          # blacklister, masterMinter, owner, pauser, rescuer
        - codehash       # eth_getProof → codeHash
        - storagehash    # eth_getProof → storageHash
  erc721:
    - name: SwapperCat
      address: 0x439d04e6BDeE57137E077E615949a5cb7f6c8Bb3
      addresses:
        - 0xabc...
      rules:
        - general        # name, symbol, paused
        - balance        # balanceOf(address) for each address above
        - codehash       # eth_getProof → codeHash
        - storagehash    # eth_getProof → storageHash
```

## CSV Output

Columns:

- `address`
- `status` (`match` / `mismatch` / `error`)
- `field`
- `l1_value`
- `l2_value`
- `error` (RPC error or mismatch reason)

Sample report: [examples/report.csv](./examples/report.csv).

## tokensync — Legacy Explorer Token Sync

Fetches token metadata from the old explorer API and inserts or updates rows in the Blockscout
`tokens` table. The token type written to Blockscout comes from the config section (`erc20` or
`erc721`, `erc1155`, etc.), not from the legacy explorer response. Existing rows are skipped by default; `--force`
updates only tokens with both `holder_count = 0` and `transfer_count = 0`.

```bash
./tokensync \
  --old-explorer-url http://<legacy-explorer-host> \
  --blockscout-dsn 'postgres://user:pass@blockscout-db.internal:5432/blockscout?sslmode=disable' \
  --config config/token-config.yml
```

### Optional Flags

- `--config` path to token config YAML (default: `config/token-config.yml`, env: `TOKEN_SYNC_CONFIG` or `TOKEN_CONFIG`)
- `--http-timeout` per-request timeout for legacy explorer calls (default: `20s`)
- `--force` update an existing token only when both `holder_count` and `transfer_count` are `0`
- `--ssh-host`, `--ssh-port`, `--ssh-user`, `--ssh-key-file` enable an SSH tunnel for the Postgres connection
- `--ssh-trust-store` path to the local `tokensync` TOFU trust store; by default it is derived from `os.UserConfigDir()` and on macOS will typically be `~/Library/Application Support/whitechain-utils/tokensync_known_hosts`

When SSH is enabled, `tokensync` dials PostgreSQL through the bastion directly. The host and port
inside `--blockscout-dsn` should be the database endpoint as seen from the SSH host, not a local
`ssh -L` forwarded port. By default `tokensync` uses TOFU and stores the first seen SSH host key
in its own local trust store instead of modifying the system `known_hosts`.
For compatibility, URL DSNs with `ssl=false` are normalized to `sslmode=disable` automatically.

### Token Config

`config/token-config.yml` contains token contract addresses grouped by target Blockscout token type:

```yaml
config:
  erc20:
    - 0xb044a2a1e3C3deb17e3602bF088811d9bDc762EA
  erc721:
    - 0xaaE5628339583dD86Ef1B1874306f29B60DB289d
  erc1155:
    - 0x1111111111111111111111111111111111111111
```

Section names are converted into Blockscout token types automatically, for example:
`erc20 -> ERC-20`, `erc721 -> ERC-721`, `erc1155 -> ERC-1155`, `wrc20 -> WRC-20`.
