# Runbook: building and booting the L2 genesis (OP Stack side)

The OP Stack half of the migration. Input is a `geth dump` of Whitechain L1 at the snapshot block
(expect roughly **150 MB** of JSON Lines for mainnet). Output is a live L2 whose genesis block
already carries the full L1 state.

For the L1 side of the operation – freezing the chain, taking and validating the dump – and for the
verification programme afterwards, see
[runbook-migration-with-repo-scripts.md](runbook-migration-with-repo-scripts.md) and
[post-migration-verification.md](post-migration-verification.md).

## The pipeline

Three different allocations are involved and they are easy to confuse. Keep them in separate files
with distinct names:

```
op-deployer apply                     ->  L1 contracts deployed
op-deployer inspect genesis           ->  genesis.base.json      [A] OP Stack predeploys only
geth dump <snapshot block>            ->  state_dump.json        [B] full L1 state, ~150 MB JSONL
dump-to-alloc.mjs  (A + B)            ->  alloc.json             [C] the combined allocation
op-node genesis l2 --l2-allocs C      ->  genesis.json + rollup.json
boot the stack                        ->  L2 live, block 0 = L1 state
```

| File | What it is | Do not |
| --- | --- | --- |
| `genesis.base.json` `[A]` | Output of `op-deployer inspect genesis`. Contains only the OP Stack predeploys. | Boot from it. It has none of the L1 state. |
| `state_dump.json` `[B]` | Output of `geth dump`. JSON Lines: a `{"root":"0x..."}` header line, then one account per line. | Pass it to `op-node`. Wrong format entirely. |
| `alloc.json` `[C]` | `[A]` and `[B]` combined into a single OP Stack allocation. | Confuse it with a genesis file. It is an allocation map, not a genesis. |
| `genesis.json` | Final L2 execution genesis emitted by `op-node`. | Edit by hand. Any edit invalidates the block 0 hash in `rollup.json`. |
| `rollup.json` | Final rollup config emitted by the same `op-node` run. | Pair with a `genesis.json` from a different run. |

### Why the merge step is not pure upstream tooling

`op-node genesis l2 --l2-allocs <file>` parses the file as an `op-chain-ops/foundry.ForgeAllocs`
document: a bare `address -> account` map where `balance` and `nonce` are **hex** quantities, `code`
is `0x`-prefixed bytes, and every storage key and value is a full 32-byte `0x`-prefixed hash. It also
**replaces** the allocation rather than merging it, so the predeploys from `[A]` must already be
inside the file that is passed.

A `geth dump` satisfies neither requirement – it is JSON Lines with a state-root header, decimal
balances, numeric nonces and unpadded storage values without `0x`. Upstream OP Stack tooling ships no
converter, and `jq` cannot stand in: it parses numbers as IEEE-754 doubles and would silently corrupt
any wei balance above 2^53. `scripts/dump-to-alloc.mjs` performs exactly this one step – convert and
merge – and everything before and after it is stock `op-deployer` and `op-node`.

```bash
export L2_CHAIN_ID=<L2_CHAIN_ID>
export L1_CHAIN_ID=11155111
export L1_RPC_URL=<L1_RPC_URL>
export WORKDIR=$(pwd)/.deployer
export SNAPSHOT_BLOCK=<SNAPSHOT_BLOCK_NUMBER>
```

---

## Step 1. Deploy the L1 contracts

`op-deployer` must be `>= v0.6.0` for Custom Gas Token v2.

```bash
op-deployer init \
  --l1-chain-id "$L1_CHAIN_ID" \
  --l2-chain-ids "$L2_CHAIN_ID" \
  --workdir "$WORKDIR" \
  --intent-type custom

# edit $WORKDIR/intent.toml: roles, fee parameters, [chains.customGasToken]

op-deployer apply \
  --workdir "$WORKDIR" \
  --l1-rpc-url "$L1_RPC_URL" \
  --private-key "$PRIVATE_KEY"
```

- [ ] `$WORKDIR/state.json` has non-zero `DisputeGameFactoryProxy` and `SystemConfigProxy`.
- [ ] `cast call <SYSTEM_CONFIG_PROXY> "isCustomGasToken()(bool)" --rpc-url "$L1_RPC_URL"` returns
      `true`.

## Step 2. Produce the base L2 genesis `[A]`

```bash
op-deployer inspect genesis       --workdir "$WORKDIR" "$L2_CHAIN_ID" > "$WORKDIR/genesis.base.json"
op-deployer inspect rollup        --workdir "$WORKDIR" "$L2_CHAIN_ID" > "$WORKDIR/rollup.base.json"
op-deployer inspect deploy-config --workdir "$WORKDIR" "$L2_CHAIN_ID" > "$WORKDIR/l2-deploy-config.json"
op-deployer inspect l1            --workdir "$WORKDIR" "$L2_CHAIN_ID" > "$WORKDIR/l1-addresses.json"
```

`genesis.base.json` and `rollup.base.json` are intermediates. Naming them `.base` prevents the
`op-node` run in Step 4 from overwriting an input it still needs, and makes it impossible to boot the
predeploy-only genesis by mistake.

- [ ] `jq '.alloc | length' "$WORKDIR/genesis.base.json"` – record this number, it is the predeploy
      count that must survive the merge.

## Step 3. Combine the L1 dump with the base allocation `[A] + [B] -> [C]`

`state_dump.json` comes from the L1 freeze-and-dump procedure and must already be validated: its
header `root` equals the `stateRoot` of the snapshot block.

```bash
node scripts/dump-to-alloc.mjs \
  --genesis "$WORKDIR/genesis.base.json" \
  --input   ./state_dump_${SNAPSHOT_BLOCK}.json \
  --output  ./alloc.json \
  2>        ./alloc-collisions.log
```

What it does, per account: decimal balance to hex quantity, numeric nonce to hex quantity, storage
keys and values left-padded to 32 bytes and `0x`-prefixed, address lowercased. Dump accounts are
streamed out first and **replace** any base entry with the same address; base entries that no dump
account touched are appended at the end. Accounts with zero balance, zero nonce, no code and no
storage are dropped unless `--no-prune-empty` is passed, and a dropped dump account leaves the base
entry for that address intact.

Every address defined by both sides is reported on stderr as a collision, which is why the run above
captures it to `alloc-collisions.log`. **Read that log before anything else.** A collision means an
L1 account has taken over an address that the OP Stack had already allocated – for a predeploy that
means its code and storage are gone from the genesis:

```
dump-to-alloc: COLLISION 0x4200000000000000000000000000000000000007 - REPLACING the base genesis entry with the L1 dump account
dump-to-alloc:     base: balance=0x0 nonce=0x0 code=10 bytes storage=1 slot(s)
dump-to-alloc:     dump: balance=0xde0b6b3a7640000 nonce=0x0 code=none storage=none
dump-to-alloc: base genesis entries: 3 total, 1 carried over, 2 replaced by dump accounts
```

The run also states when there were no collisions at all, so an empty finding is a positive result
rather than an absence of checking. See
[../README.md](../README.md#known-limitations) for which addresses are exposed and how to judge each
one.

The output is a single valid JSON object written with **one allocation entry per line**, so at 150 MB
it can be inspected with line tools instead of loading it into `jq`.

### Scale notes

| Concern | At ~150 MB |
| --- | --- |
| `dump-to-alloc.mjs` memory | Constant. Only the base allocation is held in memory; the dump is streamed. |
| `alloc.json` size | Somewhat larger than the dump – hex encoding plus 32-byte storage padding. |
| Validating with `jq empty` | Loads the whole document, roughly an order of magnitude more RAM than the file. Prefer the line-based checks below. |
| `op-node genesis l2` | Reads the allocation fully into memory as a Go map. Give the container at least 4–8 GB. |
| Booting the execution client | First `init` on a large genesis takes minutes. Do not interrupt it. |

### Checks

```bash
# Structure: first and last lines are the bare braces, entries in between.
head -1 alloc.json                       # {
tail -1 alloc.json                       # }
awk 'END{print NR-2}' alloc.json         # number of allocation entries

# Dump accounts that went in.
grep -c '"address"' ./state_dump_${SNAPSHOT_BLOCK}.json

# Collisions, straight from the run.
grep -E 'COLLISION|REPLACED|no address collisions' alloc-collisions.log

# Independent cross-check: every base entry that had code still has code.
jq -r '.alloc | to_entries[] | select(.value.code) | .key' "$WORKDIR/genesis.base.json" \
  | tr 'A-F' 'a-f' \
  | while read -r a; do
      grep -q "^\"$a\":.*\"code\"" alloc.json || echo "MISSING OR CODELESS: $a"
    done
```

- [ ] Entry count equals `dump accounts + base predeploys - overlaps - pruned empty accounts`.
- [ ] The collision log is read and every entry accounted for, or it reports no collisions.
- [ ] The cross-check prints nothing. Anything it prints is a base entry whose code was dropped, and
      is a stop condition until it is understood.
- [ ] A handful of high-value L1 accounts spot-checked for balance, nonce, code and a known storage
      slot: `grep '^"0x<address>":' alloc.json`.
- [ ] `alloc.json` backed up together with `state_dump.json` and `genesis.base.json`.

> The testnet allocation was a hand-authored list of a couple of prefunded addresses. That is not the
> mainnet case: on mainnet the allocation is the full L1 state and there is no hand-authored file. If
> extra prefunded accounts are required on top of the L1 state, add them to the dump-derived
> allocation in the same OP Stack format and re-run the checks above.

## Step 4. Generate the final `genesis.json` and `rollup.json`

Both files must come out of the same `op-node` run: `op-node` computes the L2 block 0 hash from the
allocation and writes it into `rollup.json`. A `rollup.json` from Step 2 paired with a `genesis.json`
from this step fails at boot with a genesis hash mismatch.

```bash
docker run --rm \
  --memory=8g \
  -v "$(pwd)/alloc.json:/alloc.json:ro" \
  -v "$WORKDIR/l2-deploy-config.json:/l2-deploy-config.json:ro" \
  -v "$WORKDIR/l1-addresses.json:/l1-addresses.json:ro" \
  -v "$(pwd)/out:/out" \
  us-docker.pkg.dev/oplabs-tools-artifacts/images/op-node:latest \
  op-node genesis l2 \
    --deploy-config /l2-deploy-config.json \
    --l1-deployments /l1-addresses.json \
    --l2-allocs /alloc.json \
    --outfile.l2 /out/genesis.json \
    --outfile.rollup /out/rollup.json \
    --l1-rpc "$L1_RPC_URL"
```

Equivalent with a locally built binary (`just op-node` in the Optimism monorepo):

```bash
./bin/op-node genesis l2 \
  --deploy-config "$WORKDIR/l2-deploy-config.json" \
  --l1-deployments "$WORKDIR/l1-addresses.json" \
  --l2-allocs ./alloc.json \
  --outfile.l2 ./out/genesis.json \
  --outfile.rollup ./out/rollup.json \
  --l1-rpc "$L1_RPC_URL"
```

### Checks

```bash
# Chain id and block 0 hash.
jq -r .config.chainId out/genesis.json
jq -r .genesis.l2.hash out/rollup.json

# The final allocation carries the L1 state, not just the predeploys.
jq '.alloc | length' out/genesis.json
```

- [ ] `config.chainId` equals `$L2_CHAIN_ID`.
- [ ] The allocation size in `out/genesis.json` matches the entry count of `alloc.json`.
- [ ] `genesis.l2.hash` recorded – the sequencer must report exactly this hash for block 0.
- [ ] The gas limit in `genesis.json` matches `SystemConfig.gasLimit()` on L1.
- [ ] No development or test account is present in the allocation.
- [ ] `out/genesis.json` and `out/rollup.json` backed up. From here on they are immutable: any edit
      requires re-running this step, not a manual patch.

## Step 5. Generate the challenger prestates

Prestates derive from the final `genesis.json` and `rollup.json`, so they come after Step 4.

```bash
git clone https://github.com/ethereum-optimism/optimism.git
cd optimism
git checkout op-program/v1.9.0
git submodule update --init --recursive

cp <PATH>/out/rollup.json  op-program/chainconfig/configs/${L2_CHAIN_ID}-rollup.json
cp <PATH>/out/genesis.json op-program/chainconfig/configs/${L2_CHAIN_ID}-genesis-l2.json

make reproducible-prestate
cd op-program/bin && mv prestate-mt64.bin.gz 0x<CANNON64_PRESTATE_HASH>.bin.gz
```

- [ ] Challenger mounts reference the new `0x<hash>.bin.gz` and `prestate-proof-mt64.json`.
- [ ] The absolute prestate registered on L1 for the proposer game type matches this hash. If the
      dispute game implementation on L1 was deployed against a different prestate, a new
      implementation has to be registered before withdrawals can be proven.

## Step 6. Boot the L2

1. Place the final artefacts where the stack expects them:

   ```bash
   cp out/genesis.json <ROLLUP_REPO>/envs/<env>/.deployer/genesis.json
   cp out/rollup.json  <ROLLUP_REPO>/envs/<env>/.deployer/rollup.json
   ```
2. Fill `envs/<env>/.env`: L1 RPC and beacon endpoints, operator private keys,
   `DISPUTE_GAME_FACTORY_ADDRESS` from `state.json`.
3. **Wipe execution-client and node data from any earlier run.** Booting on top of an old database is
   the most common cause of a genesis hash mismatch.

   ```bash
   make down ENV=<env>
   rm -rf ./data/op-reth-seq ./data/op-reth-rpc
   ```
4. Verify `gameImpls(<game type>)` is non-zero and that the batcher has
   `--data-availability-type=blobs`; without it the batcher will not start.
5. Start, and allow extra time for the first genesis import:

   ```bash
   make up-safe ENV=<env>
   ```

### Checks

```bash
curl -s -X POST http://127.0.0.1:8545 -H "Content-Type: application/json" \
  -d '{"jsonrpc":"2.0","method":"eth_getBlockByNumber","params":["0x0",false],"id":1}' | jq -r .result.hash
```

- [ ] Block 0 hash equals `genesis.l2.hash` from `rollup.json`.
- [ ] Block number increases at the configured block time.
- [ ] `optimism_syncStatus` shows `unsafe_l2` and `safe_l2` advancing on the sequencer and on the RPC
      node.
- [ ] A migrated L1 account returns its L1 balance, nonce and code on L2 – the first end-to-end proof
      that the state actually landed.
- [ ] Gas is paid in WBT.
- [ ] Proposer publishes output roots; batcher posts blob transactions; challenger is running.
- [ ] `SystemConfigProxy` parameters (gas limit, fee scalars, EIP-1559 parameters, `minBaseFee`,
      `unsafeBlockSigner`, batcher hash) match the specification.
- [ ] Explorer indexes blocks and transactions; predeployed contracts verified.

## Step 7. Verify the migration

Booting proves the genesis is well formed, not that the state is complete. Run the full
reconciliation and QA programme in
[post-migration-verification.md](post-migration-verification.md): `staterecon` over every account,
`tokenrecon` over tokens and NFTs, `soul-verification` over the Soul registries, then the functional
QA matrix.
