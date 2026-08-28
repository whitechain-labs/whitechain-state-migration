# Whitechain L1 to L2 State Migration

Tooling and runbooks for migrating the full Whitechain L1 state (native balances, nonces,
contract code and contract storage) into the genesis block of the Whitechain OP Stack L2, and for
verifying that the migrated state matches L1 afterwards.

The migration follows a **freeze-and-dump** strategy: L1 block production is stopped at a snapshot
block, the full state trie is exported, the export is transformed into an OP Stack genesis
allocation, and the L2 is booted from that genesis. No transaction history, logs or mempool content
is migrated.

## Repository layout

| Path | Purpose |
| --- | --- |
| `scripts/dump-to-alloc.mjs` | Converts a geth state dump (JSONL) into an OP Stack genesis `alloc` map and merges it with the base L2 allocation. |
| `scripts/insert-alloc-into-config.mjs` | Streams an `alloc` map directly into a genesis config JSON. Legacy path only – on the default path `op-node` regenerates `genesis.json`. |
| `staterecon/` | Go tooling for post-migration reconciliation: `staterecon` (accounts), `tokenrecon` (tokens), `tokensync` (explorer token metadata). See [staterecon/README.md](staterecon/README.md). |
| `soul-verification/` | Hardhat/Mocha RPC tests comparing SoulDrop / Soul registry state between the pre-migration and post-migration deployments. See [soul-verification/README.md](soul-verification/README.md). |
| `docs/` | Step-by-step migration runbooks and the verification runbook. |

## Which runbook to use

There are two ways to produce the migrated `genesis.json` / `rollup.json`. Everything else – the L1
freeze, the state dump, the L2 stack boot and the post-migration verification – is identical.

| Runbook | Genesis is built with | Use when |
| --- | --- | --- |
| [Migration with repository scripts](docs/runbook-migration-with-repo-scripts.md) | `op-deployer` + `scripts/dump-to-alloc.mjs` + `op-node genesis l2` | **Default.** Any migration that carries real L1 state (devnet, testnet, mainnet). |
| [Migration with upstream OP Stack tooling only](docs/runbook-migration-with-op-tooling.md) | `op-deployer` + `op-node genesis l2` (+ `jq`) | The allocation is small and hand-authored (for example a prefunded treasury and faucet on a fresh chain) and is already written in the `op-node` allocs format. |

Post-migration verification is shared by both runbooks and lives in
[docs/post-migration-verification.md](docs/post-migration-verification.md).

### Can `op-node` do this on its own?

No, not for a real state migration. `op-node genesis l2 --l2-allocs <file>` reads the file as an
`op-chain-ops/foundry.ForgeAllocs` document, which is a bare `address -> account` map with
**hex-encoded** fields:

| Field | Type expected by `op-node` |
| --- | --- |
| `balance` | hex quantity (`hexutil.U256`) |
| `nonce` | hex quantity (`hexutil.Uint64`) |
| `code` | `0x`-prefixed bytes |
| `storage` | map of full 32-byte `0x`-prefixed key to full 32-byte `0x`-prefixed value |

A `geth dump` of L1 does not satisfy any of that. It is JSON Lines (one account per line, plus a
header line carrying the state root), balances are **decimal** strings, nonces are JSON numbers, and
storage values are unpadded hex without the `0x` prefix. On top of that, `op-node` **replaces** the
allocation instead of merging it, so the OP Stack predeploys that `op-deployer` put into the base
`genesis.json` must already be present in the file you pass to `--l2-allocs`.

`scripts/dump-to-alloc.mjs` exists exactly for those two jobs – format conversion and merge with the
base allocation – and upstream OP Stack tooling ships no equivalent. It is required. What is *not*
required when `op-node` is used is `scripts/insert-alloc-into-config.mjs`: `op-node` regenerates
`genesis.json` and `rollup.json` together, so the block 0 hash stays consistent automatically.

## Prerequisites

| Tool | Version / note |
| --- | --- |
| `op-deployer` | `>= v0.6.0` (required for Custom Gas Token v2) |
| `op-node` | Docker image `us-docker.pkg.dev/oplabs-tools-artifacts/images/op-node:latest`, or built from the Optimism monorepo with `just op-node` |
| Node.js | `>= 18` (the scripts use only the standard library, no dependencies) |
| Go | `>= 1.25` for `staterecon` |
| Docker + Docker Compose | L1 dump, `op-node` invocation, L2 stack |
| `cast` (Foundry) | on-chain checks |
| `jq` | JSON inspection |

The L1 node used for the export must be a **full, non-pruned** node that was synced with
`--cache.preimages`, must expose the `debug` API (or IPC), and must have block production stopped
before the dump begins.

## Script reference

### `scripts/dump-to-alloc.mjs`

Merges a geth JSONL state dump into the `alloc` map of a genesis file and writes the combined
allocation.

```bash
node scripts/dump-to-alloc.mjs \
  --genesis genesis.json \
  --input state_dump.json \
  --output alloc.json
```

| Flag | Meaning |
| --- | --- |
| `--genesis <path>` | Required. Base genesis JSON from `op-deployer inspect genesis`; its `alloc` (or `allocs`) object is the starting point. |
| `--input <path>` | Required. JSONL state dump from `geth dump <block>`. The header line (`{"root":"0x..."}`) is ignored. |
| `--output <path>` | Output allocation JSON. Defaults to stdout. |
| `--no-prune-empty` | Keep accounts whose balance, nonce, code and storage are all empty. By default such accounts are dropped. |

Behaviour worth knowing:

- Addresses are lowercased and `0x`-prefixed; balances and nonces become hex quantities; storage keys
  and values are left-padded to 32 bytes.
- Dump entries **overwrite** entries of the same address in the base genesis allocation. Verify after
  the merge that every OP Stack predeploy (`0x42000000000000000000000000000000000000xx`) survived.
- The output is a JSON object written with **one alloc entry per line**:

  ```json
  {
  "0x1111111111111111111111111111111111111111":{"balance":"0x48c27395000","nonce":"0x3"},
  "0x22222222222222222222222222222222222222aa":{"balance":"0x0","nonce":"0x0","code":"0x6060"}
  }
  ```

  It is valid JSON for `jq` and for `op-node genesis l2 --l2-allocs`, and it is the format
  `scripts/insert-alloc-into-config.mjs` reads, so the two scripts chain directly.
- Dump accounts are streamed straight to the output as they are read; only the base genesis
  allocation is held in memory, so peak memory does not grow with the size of the state dump.
- A run that fails part-way deletes its partial `--output` file rather than leaving truncated,
  syntactically invalid JSON behind.

### `scripts/insert-alloc-into-config.mjs`

Streams an allocation map into a genesis config JSON without loading the whole allocation into
memory. Only needed on the legacy path where `genesis.json` is patched directly instead of being
regenerated by `op-node`.

```bash
node scripts/insert-alloc-into-config.mjs \
  --config genesis.json \
  --alloc alloc.json \
  --output genesis.updated.json \
  --stats --progress
```

| Flag | Meaning |
| --- | --- |
| `--config <path>` | Required. Genesis config JSON. |
| `--alloc <path>` | Required. Allocation map with **one entry per line**, as written by `dump-to-alloc.mjs`. Any other line shape is a hard error – see [Known limitations](#known-limitations). |
| `--output <path>` | Output file. Defaults to stdout. |
| `--pretty` | Pretty-print the output. Slower and larger. |
| `--stats` | Print how many entries were added and how many were skipped as already present. |
| `--overwrite-existing` | Let allocation entries win over entries already in `config.alloc`. Default is to keep the existing entry. |
| `--progress` / `--progress-interval-ms <ms>` | Progress reporting to stderr. |

## Known limitations

1. **Empty accounts are pruned by default.** Accounts with zero balance, zero nonce, no code and no
   storage are dropped unless `--no-prune-empty` is passed. Account counts between the dump and the
   allocation will therefore differ by that number.
2. **On an address collision the L1 dump wins.** An address defined by both the base L2 allocation
   and the L1 dump is a collision, and the dump account replaces the base entry – so an OP Stack
   predeploy hit this way loses its code and storage. The one exception is a dump account that is
   empty and gets pruned: the base entry survives.

   `dump-to-alloc.mjs` reports every collision on stderr as it happens, with a before/after summary,
   repeats the full list at the end of the run, and states explicitly when there were none. Capture
   it and read it before booting anything:

   ```bash
   node scripts/dump-to-alloc.mjs \
     --genesis genesis.base.json \
     --input state_dump.json \
     --output alloc.json \
     2> alloc-collisions.log
   ```

   ```
   dump-to-alloc: COLLISION 0x4200000000000000000000000000000000000007 - REPLACING the base genesis entry with the L1 dump account
   dump-to-alloc:     base: balance=0x0 nonce=0x0 code=10 bytes storage=1 slot(s)
   dump-to-alloc:     dump: balance=0xde0b6b3a7640000 nonce=0x0 code=none storage=none
   dump-to-alloc: base genesis entries: 3 total, 1 carried over, 2 replaced by dump accounts
   ```

   Two groups of addresses are exposed, and they carry very different risk:

   - **OP Stack predeploys**, `0x42000000000000000000000000000000000000xx` plus
     `0xDeadDeAddeAddEAddeadDEaDDEAdDeaDDeAD0000`. Nobody holds a key for these, but anyone can send
     WBT to one on L1, which creates a codeless account that the dump then carries over and that
     silently overwrites the predeploy. This is the dangerous case.
   - **Preinstalls at ordinary addresses** – Multicall3 `0xcA11bde05977b3631167028862bE2a173976CA11`,
     Create2Deployer `0x13b0D85CcB8bf860b6b79AF3029fCA081AE9beF2`, DeterministicDeploymentProxy
     `0x4e59b44847b379578588920cA78FbF26c0B4956C`, Permit2, the Safe singletons, the ERC-4337
     EntryPoints, CreateX. These are deployed at the same address on every EVM chain, so if any of
     them exists on Whitechain L1 the collision is expected rather than exceptional. Compare the
     bytecode on both sides: identical code means the collision is harmless, different code means
     the L1 version is about to become the L2 version.

   The authoritative list for a given deployment is not this document but the base allocation
   itself: `jq -r '.alloc | keys[]' genesis.base.json`.
3. **`insert-alloc-into-config.mjs` reads only one-entry-per-line allocation files.** Its streaming
   parser requires every entry on its own line (`"0x<40 hex>":{...}`), which is exactly what
   `dump-to-alloc.mjs` writes – the two chain without an intermediate step. Pretty-printed
   allocations, and allocations written as a single line, are **rejected**: the script prints the
   offending line number, exits non-zero and removes the partial output instead of writing invalid
   JSON. An `alloc.json` produced by an older revision of `dump-to-alloc.mjs` (pretty-printed with
   2-space indentation) must be regenerated, or reformatted:

   ```bash
   node --max-old-space-size=16384 -e 'const fs=require("fs");const a=JSON.parse(fs.readFileSync(process.argv[1],"utf8"));const o=fs.createWriteStream(process.argv[2]);o.write("{\n");let first=true;for(const[k,v]of Object.entries(a)){o.write(`${first?"":",\n"}"${k}":${JSON.stringify(v)}`);first=false;}o.write("\n}\n");o.end();' alloc.json alloc.lines.json
   ```

   The reformat loads the whole allocation into memory; regenerating with `dump-to-alloc.mjs` does
   not. This limitation does not affect the default runbook, which passes `alloc.json` straight to
   `op-node`.


