# Post-migration verification

Shared verification runbook. It applies unchanged to both genesis paths
([repository scripts](runbook-migration-with-repo-scripts.md) and
[upstream OP tooling](runbook-migration-with-op-tooling.md)), because verification compares the live
L2 against L1 and does not depend on how the genesis was built.

Goal: zero data loss, full consistency between L1 at the snapshot block and L2, and a recorded audit
trail.

## Prerequisites

| Requirement | Note |
| --- | --- |
| L1 archive RPC | Must still serve state at the snapshot block, with `debug_accountRange` and `eth_getProof` enabled. |
| L2 RPC | Sequencer or RPC node, with `eth_getProof` enabled. |
| Snapshot block | The L1 block frozen during migration, as a hex tag. |
| L2 block | Pin to a specific L2 block so a moving head does not produce false mismatches. |
| Contract inventory | The list of critical contracts, tokens and NFTs to reconcile. |

```bash
export L1_RPC=<L1_ARCHIVE_RPC>
export L2_RPC=<L2_RPC>
export L1_BLOCK=<SNAPSHOT_BLOCK_HEX>
export L2_BLOCK=<L2_BLOCK_HEX>
```

## Layer 0 – Chain sanity

Run first; a failure here invalidates everything downstream.

```bash
# chain id
cast chain-id --rpc-url "$L2_RPC"

# block production and block time
curl -s -X POST "$L2_RPC" -H "Content-Type: application/json" \
  -d '{"jsonrpc":"2.0","method":"eth_blockNumber","params":[],"id":1}'

# derivation health
curl -s <OP_NODE_RPC> -H "Content-Type: application/json" \
  -d '{"jsonrpc":"2.0","method":"optimism_syncStatus","params":[],"id":1}' | jq
```

- [ ] Chain id matches the specification.
- [ ] Block number increases; block time matches the configured value.
- [ ] `unsafe_l2` and `safe_l2` both advance.
- [ ] WBT is the gas token – a test transaction charges WBT.
- [ ] `eth_subscribe` / `eth_unsubscribe` work over WebSocket.

## Layer 1 – Account reconciliation (`staterecon`)

Walks every L1 account with `debug_accountRange` and compares it against L2.

```bash
cd staterecon
go build -o staterecon ./cmd/staterecon

./staterecon \
  --l1-rpc "$L1_RPC" \
  --l2-rpc "$L2_RPC" \
  --l1-block "$L1_BLOCK" \
  --l2-block "$L2_BLOCK" \
  --output-dir ./out \
  --print-mismatches
```

Compared per account:

| Field | L1 source | L2 source |
| --- | --- | --- |
| `balance` | `debug_accountRange` | `eth_getBalance` |
| `nonce` | `debug_accountRange` | `eth_getTransactionCount` |
| `code_hash` | `eth_getProof` | `eth_getProof` |
| `storage_hash` | `eth_getProof` | `eth_getProof` |

Code hash and storage hash are only compared for contract accounts. Results stream to CSV with
columns `address, status, field, l1_value, l2_value, error`.

Tuning for large state: `--page-size`, `--batch-size`, `--rpc-batch-size`, `--rpc-timeout`. Use
`--account-range-start` to resume an interrupted run.

- [ ] Zero rows with `status=mismatch`.
- [ ] Zero rows with `status=error` – an `error` row means the comparison did not happen, not that it
      passed.
- [ ] Account count processed matches the expectation from the dump, allowing for empty accounts
      pruned during the allocation build.
- [ ] The CSV is archived with the migration record.

Expected non-issues: OP Stack predeploys exist only on L2 and have no L1 counterpart.

## Layer 2 – Token and NFT reconciliation (`tokenrecon`)

Calls view functions on both chains for the contracts listed in a config file and compares raw
return data, plus storage slots via `eth_getStorageAt`.

```bash
cd staterecon
go build -o tokenrecon ./cmd/tokenrecon
cp config/config.example.yml config/config.yml   # then edit

./tokenrecon \
  --l1-rpc "$L1_RPC" \
  --l2-rpc "$L2_RPC" \
  --l1-block "$L1_BLOCK" \
  --l2-block "$L2_BLOCK" \
  --config config/config.yml \
  --output-dir ./token_out
```

Config shape:

```yaml
config:
  erc20:
    - name: USDC
      address: 0x<TOKEN_ADDRESS>
      addresses:        # holders checked with balanceOf / nonces
        - 0x<HOLDER>
      storages:         # raw slots compared with eth_getStorageAt
        - 0x<SLOT>
      rules:
        - general       # typehashes, DOMAIN_SEPARATOR, currency, decimals, name, symbol, totalSupply, version
        - balance       # balanceOf(address), nonces(address)
        - roles         # blacklister, masterMinter, owner, pauser, rescuer
        - codehash
        - storagehash
  erc721:
    - name: <COLLECTION>
      address: 0x<TOKEN_ADDRESS>
      addresses:
        - 0x<HOLDER>
      rules: [general, balance, codehash, storagehash]
```

- [ ] Every token and NFT from the migration inventory is present in the config.
- [ ] `totalSupply` matches on both chains for every token.
- [ ] Roles and owners match.
- [ ] Code hash and storage hash match.
- [ ] Zero `mismatch` and zero `error` rows.

## Layer 3 – SoulDrop and Soul registries (`soul-verification`)

RPC tests comparing the pre-migration deployment (`ORIGINAL_*`) against the post-migration deployment
(`TARGET_*`). Requires the SoulDrop contract sources so `artifacts/` and `artifacts/build-info/`
exist for the storage-layout checks.

```bash
cd soul-verification
npm run compile
cp .env.example .env          # fill ORIGINAL_* / TARGET_* RPC URLs and contract addresses

npx hardhat test test/migrationStateDiff.rpc.ts
```

Spot-check variant for a fast signal on a subset of souls:

```bash
MIGRATION_SAMPLE_SOUL_IDS=1,42,100 npx hardhat test test/migrationStateDiffSample.rpc.ts
```

Useful bounds on a long run: `MIGRATION_MAX_SOUL_IDS`, `MIGRATION_MAX_HOLD_LEVEL_KEY`,
`MIGRATION_ENABLE_REVERSE_INDEX_CHECKS`, `ORIGINAL_BLOCK_NUMBER` / `TARGET_BLOCK_NUMBER`. The full
list is in [../soul-verification/README.md](../soul-verification/README.md). The Mocha timeout is 50
minutes because of the RPC volume.

- [ ] Full migration state diff passes with storage-layout checks enabled.
- [ ] Pinned block numbers are used on both sides so a moving head cannot cause a false diff.

## Layer 4 – Explorer token metadata (`tokensync`)

Backfills the Blockscout `tokens` table from the legacy explorer API so migrated tokens are labelled
correctly in the new explorer. This changes explorer data, not chain state.

```bash
cd staterecon
go build -o tokensync ./cmd/tokensync
cp config/token-config.example.yml config/token-config.yml   # then edit

./tokensync \
  --old-explorer-url <LEGACY_EXPLORER_URL> \
  --blockscout-dsn 'postgres://<user>:<pass>@<host>:5432/blockscout?sslmode=disable' \
  --config config/token-config.yml
```

The token type written to Blockscout comes from the config section (`erc20`, `erc721`, `erc1155`),
not from the legacy response. Existing rows are skipped unless `--force` is passed, which updates
only tokens with `holder_count = 0` and `transfer_count = 0`. SSH tunnelling flags are documented in
[../staterecon/README.md](../staterecon/README.md).

- [ ] Token names, symbols and types render correctly in the explorer.
- [ ] No pre-existing row with real holder or transfer counts was overwritten.

## Layer 5 – Functional QA

Automated unless noted. Priorities follow the QA coverage map: P0 blocks mainnet.

### Genesis validation

- [ ] RPC responds with valid data on all documented methods.
- [ ] Chain id correct; WBT active as gas token.
- [ ] Block number increases; block production at the configured rate.
- [ ] 100 sampled wallets, with positive and zero balances: WBT balance, token balances, NFT
      balances and nonces match L1.
- [ ] Critical contracts: balance, nonce, code, storage, owners and roles match L1.
- [ ] Tokens and NFTs: code, storage, minters and total supply match L1.
- [ ] Proxy contracts: implementation slot (EIP-1967) matches L1.

### Core chain operations

- [ ] Send, approve, mint and burn succeed for ERC-20, ERC-721 and ERC-1155.
- [ ] Gas mechanics: `eth_gasPrice`, `eth_estimateGas` for a plain transfer and for a call with
      calldata, `eth_maxPriorityFeePerGas`, `gasUsed <= gasLimit`.
- [ ] Negative cases: intrinsic gas too low, insufficient funds, `gasPrice = 0`, wrong chain id,
      duplicate nonce, `max uint256` transfer, WBT refunded on revert.
- [ ] Access control: `onlyOwner` from a non-owner reverts; proxy admin functions from a regular user
      revert; unauthorised `upgradeTo` reverts and leaves the implementation slot unchanged.

### Bridges and cross-chain

- [ ] Canonical bridge deposit L1 to L2, including a recipient different from the sender, 1 wei and
      the contract maximum.
- [ ] Supply invariant: L1 locked plus L2 minted equals the original total supply.
- [ ] ETH deposits through the L1 bridge revert (custom gas token chain).
- [ ] Withdrawal proof replay reverts; `finalizeWithdrawal` before the challenge period reverts.
- [ ] Whitechain Portal bridge: implementations, mappings, owners and balances match L1; happy paths
      and admin-only negative paths behave as specified.

### Platform

- [ ] Explorer shows blocks, transactions, wallets and contracts; predeployed contracts verified.
- [ ] SoulDrop and staking: contract state matches L1; claim and liquidity flows succeed.
- [ ] DEX: pairs, reserves, LP total supply, add and remove liquidity, swap, allowances.
- [ ] Wallets: network add, import, balances, history, transactions, WalletConnect to a dApp.
- [ ] Off-chain services repointed at L2 and returning current data.

### Resilience and load

- [ ] Transaction submitted while the sequencer is down lands via
      `OptimismPortal.depositTransaction()` forced inclusion.
- [ ] Transaction with a nonce gap queues instead of executing.
- [ ] 100 or more simultaneous deposits processed with nothing lost.
- [ ] Dust test: many 1 wei transfers, balances still reconcile.

### Operator and configuration audit

- [ ] `SystemConfig`: gas limit, fee scalars, batcher hash, `unsafeBlockSigner`, resource config
      match the specification, and the gas limit matches the L2 genesis block.
- [ ] `SuperchainConfig.paused() == false`; guardian address is the expected multisig; pause and
      unpause behave correctly on a test environment.
- [ ] Proposer publishes output roots at the configured interval and its wallet is funded.
- [ ] Batcher submits type-3 blob transactions.
- [ ] Every privileged role maps to the intended multisig or operator address, with no leftover test
      keys – in particular any L1 administrative addresses that carried special mint rights.
- [ ] Contracts that depend on `block.number` are reviewed, since L2 block numbering restarts.

## Acceptance criteria

All of the following must hold before sign-off:

- [ ] Every critical balance matches exactly after finality.
- [ ] All storage slots, including proxy implementation slots, are identical where expected.
- [ ] `staterecon`, `tokenrecon` and `soul-verification` report zero discrepancies.
- [ ] All P0 QA cases pass.
- [ ] Output roots are published and unchallenged.
- [ ] Monitoring and alerting are live for proposer interval, batcher submissions, sequencer health,
      peer count and any storage root mismatch, with alerting on any discrepancy greater than zero.
- [ ] Reports and artefacts archived: `staterecon` CSV, `tokenrecon` CSV, `soul-verification` output,
      snapshot block number, hash and state root, checksums of the dump, allocation, `genesis.json`
      and `rollup.json`, L2 block 0 hash, prestate hash and tool versions.
