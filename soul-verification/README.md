# State migration tests

> [NOTE]
> State migration tests require smart contracts (it will work once they are copied into repository with SoulDrop smart contracts)

These checks compare on-chain state between a **pre-migration** deployment (`ORIGINAL_*`) and a **post-migration** deployment (`TARGET_*`) over JSON-RPC. They live under `test/` as Mocha tests and reuse `migrationStateSnapshot.ts`.

## Dependencies

| Package | Purpose |
|--------|---------|
| `hardhat` | Test runner, artifacts, `artifacts` import in snapshot code |
| `@nomiclabs/hardhat-ethers` / `@nomiclabs/hardhat-waffle` | Hardhat Ethereum integration |
| `@nomicfoundation/hardhat-network-helpers` | Hardhat helpers |
| `hardhat-deploy` | Deploy tooling (project Hardhat setup) |
| `typescript` / `ts-node` | Run `.ts` tests and scripts |
| `@types/node` / `@types/mocha` / `@types/chai` | TypeScript typings |
| `mocha` / `chai` | Test framework and assertions |
| `dotenv` | Load `.env` (via `loadEnv()` in tests) |
| `@openzeppelin/contracts` | Solidity dependency for `compile` |
| `solidity-coverage` | Coverage plugin |

`ethers` is pulled in as a dependency of the Hardhat stack (used by `migrationStateSnapshot.ts`).

## Before you run

1. **Compile contracts** so `artifacts/` and `artifacts/build-info/` exist. Storage layout checks (enabled by default) need build info:

   ```bash
   npm run compile
   ```

2. **Configure environment variables** (see below). You can put them in a `.env` file in the project root; tests call `loadEnv()` which uses `dotenv`.

## Required environment variables

| Variable | Description |
|----------|-------------|
| `ORIGINAL_RPC_URL` | JSON-RPC URL for the chain that still has the legacy deployment |
| `TARGET_RPC_URL` | JSON-RPC URL for the chain with the migrated deployment |
| `SOUL_REGISTRY_CONFIG_ORIGINAL` / `SOUL_REGISTRY_CONFIG_TARGET` | SoulRegistryConfig addresses |
| `SOUL_REGISTRY_ORIGINAL` / `SOUL_REGISTRY_TARGET` | SoulRegistry addresses |
| `SOUL_ATTRIBUTE_REGISTRY_ORIGINAL` / `SOUL_ATTRIBUTE_REGISTRY_TARGET` | SoulAttributeRegistry addresses |
| `SOUL_BOUND_TOKEN_REGISTRY_ORIGINAL` / `SOUL_BOUND_TOKEN_REGISTRY_TARGET` | SoulBoundTokenRegistry addresses |
| `HOLD_AMOUNT_ORIGINAL` / `HOLD_AMOUNT_TARGET` | HoldAmount attribute contract addresses |
| `IS_VERIFIED_ORIGINAL` / `IS_VERIFIED_TARGET` | IsVerified attribute contract addresses |
| `SOUL_DROP_ORIGINAL` / `SOUL_DROP_TARGET` | SoulDrop addresses |

Addresses must be checksummed or valid `0x` hex strings (the snapshot utilities normalize them).

### Optional environment variables

| Variable | Description |
|----------|-------------|
| `EARLYBIRD_ORIGINAL` / `EARLYBIRD_TARGET` | If both set, maps EarlyBird collection addresses between chains for token comparisons |
| `ORIGINAL_BLOCK_NUMBER` / `TARGET_BLOCK_NUMBER` | Pin reads to a specific block |
| `MIGRATION_MAX_SOUL_IDS` | Limit how many soul IDs are checked (from 1 upward); full diff test only |
| `MIGRATION_MAX_HOLD_LEVEL_KEY` | Cap hold-level key range for SoulDrop comparisons |
| `MIGRATION_ENABLE_STORAGE_LAYOUT_CHECKS` | `1`/`true` or `0`/`false` (default: `true` in tests) |
| `MIGRATION_ENABLE_REVERSE_INDEX_CHECKS` | `1`/`true` or `0`/`false` — full test defaults `true`; sample test defaults `false` (reverse scans are expensive) |
| `MIGRATION_LOG_PROGRESS` | Set to `0` to reduce console progress output |
| `MIGRATION_LOG_EVERY` | Progress log interval (default `25`) |
| `MIGRATION_SAMPLE_SOUL_IDS` | **Required for the sample test only** — comma-separated soul IDs (e.g. `1,42,100`) |

## Running the tests

Tests use a **50 minute** Mocha timeout (`3000000` ms) because they perform many RPC calls.

### Full migration state diff

Compares migrated state across the configured contracts (optionally bounded by `MIGRATION_MAX_SOUL_IDS` and other flags above).

```bash
npx hardhat test test/migrationStateDiff.rpc.ts
```

### Sampled soul IDs (spot-check)

Same contract configuration as the full run, but per-soul attribute/SBT data is restricted to `MIGRATION_SAMPLE_SOUL_IDS`. Global contract fields are still compared in full. **Requires** `MIGRATION_SAMPLE_SOUL_IDS`.

```bash
npx hardhat test test/migrationStateDiffSample.rpc.ts
```