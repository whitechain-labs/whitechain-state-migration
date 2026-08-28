# Runbook: L1 to L2 migration with repository scripts

End-to-end procedure for migrating the full Whitechain L1 state into an OP Stack L2 genesis using
`op-deployer`, `scripts/dump-to-alloc.mjs` from this repository, and `op-node genesis l2`.

This is the default path. Use it for any migration that carries real L1 state. If your allocation is
a small hand-authored list rather than an L1 state dump, see
[runbook-migration-with-op-tooling.md](runbook-migration-with-op-tooling.md) instead.

Post-migration verification is documented separately in
[post-migration-verification.md](post-migration-verification.md).

## Overview

| Phase | Steps | Reversible |
| --- | --- | --- |
| Before migration | 0. Readiness, 1. Deploy L1 contracts, 2. Base genesis artefacts | Yes |
| Migration | 3. Freeze L1, 4. Dump L1 state, 5. Build allocation, 6. Regenerate genesis and rollup, 7. Prestates, 8. Boot L2 | Step 3 onwards is a public commitment |
| After migration | 9. Health checks, 10. Reconciliation, 11. Functional QA, 12. Sign-off | – |

Fill in the environment values once and reuse them throughout:

```bash
export L2_CHAIN_ID=<L2_CHAIN_ID>          # for example 1874 (testnet) or 2625 (devnet)
export L1_CHAIN_ID=11155111               # Ethereum Sepolia
export L1_RPC_URL=<L1_RPC_URL>
export L1_BEACON_URL=<L1_BEACON_URL>
export WORKDIR=$(pwd)/.deployer
```

---

## Phase 1 – Before migration

### Step 0. Confirm readiness

- [ ] L1 RPC and L1 Beacon endpoints reachable from the deployer host and from every rollup service.
- [ ] Deployer address funded on L1.
- [ ] Operator addresses prepared and recorded: admin (ProxyAdmin owner), SystemConfig owner,
      batcher, proposer, challenger, unsafe block signer, fee vault recipients.
- [ ] Infrastructure for the L2 stack provisioned (sequencer, RPC node, batcher, proposer,
      challenger, explorer), with persistent volumes for `op-node` state
      (`--p2p.peerstore.path`, `--p2p.discovery.path`, `--safedb.path`).
- [ ] P2P ports open (TCP + UDP) on the sequencer and RPC nodes.
- [ ] The L1 node that will be dumped is a **full, non-pruned** node, synced with
      `--cache.preimages`, with the `debug` API or IPC available.
- [ ] Backup destination prepared for `state_dump.json`, `alloc.json`, `genesis.json`, `rollup.json`.
- [ ] Communication plan agreed for the L1 read-only window.

### Step 1. Deploy L1 contracts with op-deployer

`op-deployer` must be `>= v0.6.0` – Custom Gas Token v2 requires it.

```bash
op-deployer init \
  --l1-chain-id "$L1_CHAIN_ID" \
  --l2-chain-ids "$L2_CHAIN_ID" \
  --workdir "$WORKDIR" \
  --intent-type custom
```

Edit `$WORKDIR/intent.toml` with the agreed chain parameters. The parts that matter for a Whitechain
migration:

```toml
configType = "custom"
l1ChainID = 11155111
fundDevAccounts = false

[[chains]]
  id = "0x<L2_CHAIN_ID_AS_32_BYTE_HEX>"

  # fee and EIP-1559 parameters per the environment specification
  eip1559DenominatorCanyon = 250
  eip1559Denominator       = 100
  eip1559Elasticity        = 6
  gasLimit                 = 40000000
  minBaseFee               = 5000000000

  [chains.roles]
    l1ProxyAdminOwner = "0x<ADMIN>"
    l2ProxyAdminOwner = "0x<ADMIN>"
    systemConfigOwner = "0x<SYSTEM_CONFIG_OWNER>"
    unsafeBlockSigner = "0x<UNSAFE_BLOCK_SIGNER>"
    batcher           = "0x<BATCHER>"
    proposer          = "0x<PROPOSER>"
    challenger        = "0x<CHALLENGER>"

  # Required: without this section a standard ETH chain is deployed.
  [chains.customGasToken]
    name   = "WhiteBIT Coin"
    symbol = "WBT"
    liquidityControllerOwner = "0x<ADMIN>"
```

Deploy:

```bash
op-deployer apply \
  --workdir "$WORKDIR" \
  --l1-rpc-url "$L1_RPC_URL" \
  --private-key "$PRIVATE_KEY"
```

Checks:

- [ ] `$WORKDIR/state.json` exists and contains non-zero `DisputeGameFactoryProxy` and
      `SystemConfigProxy` addresses.
- [ ] Custom gas token flag is set on L1:

      ```bash
      cast call <SYSTEM_CONFIG_PROXY> "isCustomGasToken()(bool)" --rpc-url "$L1_RPC_URL"
      # expected: true
      ```

### Step 2. Generate the base genesis artefacts

```bash
op-deployer inspect genesis       --workdir "$WORKDIR" "$L2_CHAIN_ID" > "$WORKDIR/genesis.json"
op-deployer inspect rollup        --workdir "$WORKDIR" "$L2_CHAIN_ID" > "$WORKDIR/rollup.json"
op-deployer inspect deploy-config --workdir "$WORKDIR" "$L2_CHAIN_ID" > "$WORKDIR/l2-deploy-config.json"
op-deployer inspect l1            --workdir "$WORKDIR" "$L2_CHAIN_ID" > "$WORKDIR/l1-addresses.json"
```

| File | Contents |
| --- | --- |
| `genesis.json` | Base L2 execution genesis, including the OP Stack predeploy allocation. |
| `rollup.json` | Base rollup configuration. |
| `l2-deploy-config.json` | L2 deploy configuration, input to `op-node genesis l2`. |
| `l1-addresses.json` | Deployed L1 contract addresses, input to `op-node genesis l2`. |

The `genesis.json` produced here is the **base allocation** that Step 5 merges the L1 state into. Do
not boot anything from it.

---

## Phase 2 – Migration

Everything from here on is time-boxed: L1 is read-only for the duration.

### Step 3. Freeze L1

1. Stop accepting new transactions (disable the mempool / stop the block producer) so no block is
   produced after the snapshot block.
2. Record the final chain head and keep it with the migration record:

   ```bash
   curl -s -X POST -H "Content-Type: application/json" \
     --data '{"jsonrpc":"2.0","method":"eth_getBlockByNumber","params":["latest",false],"id":1}' \
     http://<L1_NODE>:8545 | jq '{number:.result.number, hash:.result.hash, stateRoot:.result.stateRoot, timestamp:.result.timestamp}'
   ```

   - [ ] Snapshot block number recorded (decimal and hex).
   - [ ] Snapshot block hash recorded.
   - [ ] Snapshot block `stateRoot` recorded – Step 4 validates the dump against it.

### Step 4. Dump the L1 state

Stop the node gracefully so geth flushes the dirty trie cache. Never use `docker compose kill`.

```bash
docker compose stop node
docker compose ps -a | grep node        # expect "Exited (0)"
```

Confirm the shutdown was clean by checking that `geth/chaindata/LOCK` is gone; if it is still there,
remove it before dumping.

Run the dump:

```bash
BLOCK=<SNAPSHOT_BLOCK_NUMBER>
OUTPUT="./state_dump_${BLOCK}.json"

docker run --rm \
  -v "$(pwd)/data:/root/.ethereum" \
  --entrypoint geth \
  whitebit/wbt:<version> \
  --wbt-testnet \
  --cache.preimages \
  --datadir /root/.ethereum \
  dump "$BLOCK" > "$OUTPUT"
```

Expect 40–90 minutes depending on state size. Progress lines look like
`INFO Trie dumping in progress accounts=...`.

Validate the dump before going further:

- [ ] The `root` in the dump header line equals the `stateRoot` of the snapshot block.

      ```bash
      head -1 "$OUTPUT" | jq -r .root
      ```
- [ ] Account count, contract count and the sum of balances are recorded and the balance sum matches
      the known native supply.
- [ ] `state_dump_<BLOCK>.json` is backed up off-host before any further processing.

Restart the L1 node in read-only mode once the dump is safely copied. Keep an archive L1 RPC
available – reconciliation in Phase 3 reads from it.

### Step 5. Build the merged allocation

```bash
node scripts/dump-to-alloc.mjs \
  --genesis "$WORKDIR/genesis.json" \
  --input "./state_dump_${BLOCK}.json" \
  --output ./alloc.json
```

This converts every dump record into genesis `alloc` form (hex balance and nonce, 32-byte padded
storage keys and values) and merges it onto the base allocation from `genesis.json`. Accounts are
streamed to `alloc.json` as the dump is read, so memory stays flat regardless of state size, and
the result is written with one alloc entry per line. A failed run deletes its partial `alloc.json`
rather than leaving truncated JSON behind.

Checks:

- [ ] `jq empty alloc.json` passes.
- [ ] Entry count is `base predeploys + dump accounts - pruned empty accounts`:

      ```bash
      jq 'length' alloc.json
      jq '.alloc | length' "$WORKDIR/genesis.json"
      grep -c '"address"' "./state_dump_${BLOCK}.json"
      ```

      One entry per line means the count can also be taken without parsing the whole file, which
      matters once `alloc.json` runs to several gigabytes:

      ```bash
      echo $(( $(wc -l < alloc.json) - 2 ))
      ```
- [ ] Every OP Stack predeploy from the base genesis is still present and still carries its code:

      ```bash
      jq -r '.alloc | keys[]' "$WORKDIR/genesis.json" \
        | while read -r a; do
            jq -e --arg a "$(echo "$a" | tr 'A-F' 'a-f')" '.[$a].code' alloc.json > /dev/null \
              || echo "MISSING OR CODELESS: $a"
          done
      ```
- [ ] Spot-check a few high-value L1 accounts: balance, nonce, code and a known storage slot.
- [ ] `alloc.json` is backed up.

If you need empty accounts preserved for an exact account-count match, rerun with
`--no-prune-empty`.

### Step 6. Regenerate genesis.json and rollup.json

`op-node` recomputes the L2 block 0 hash from the new allocation and writes it into `rollup.json`,
which is why both files must be produced by the same command.

```bash
docker run --rm \
  -v "$(pwd)/alloc.json:/new-allocation.json" \
  -v "$WORKDIR/l2-deploy-config.json:/l2-deploy-config.json" \
  -v "$WORKDIR/l1-addresses.json:/l1-addresses.json" \
  -v "$(pwd):/out" \
  us-docker.pkg.dev/oplabs-tools-artifacts/images/op-node:latest \
  op-node genesis l2 \
    --deploy-config /l2-deploy-config.json \
    --l1-deployments /l1-addresses.json \
    --l2-allocs /new-allocation.json \
    --outfile.l2 /out/genesis.json \
    --outfile.rollup /out/rollup.json \
    --l1-rpc "$L1_RPC_URL"
```

The binary form is equivalent – build `op-node` from the Optimism monorepo with `just op-node` and
run the same subcommand with local paths.

Checks:

- [ ] Both files were rewritten in this run. A `rollup.json` from Step 2 paired with a `genesis.json`
      from Step 6 will fail at boot with a genesis hash mismatch.
- [ ] `jq -r .genesis.l2.hash rollup.json` is recorded; the sequencer must report exactly this hash
      for block 0.
- [ ] `jq -r .config.chainId genesis.json` equals `$L2_CHAIN_ID`.
- [ ] Both files are backed up.

> Legacy alternative, not recommended. `scripts/insert-alloc-into-config.mjs` patches an allocation
> into `genesis.json` directly, after which the block 0 hash must be recomputed by booting the
> execution client alone and `rollup.json` must be edited by hand. Use it only when `op-node` is not
> available. It reads the `alloc.json` from Step 5 directly – no reformatting step – and rejects an
> allocation in any other line format instead of writing invalid JSON:
>
> ```bash
> node scripts/insert-alloc-into-config.mjs \
>   --config "$WORKDIR/genesis.json" \
>   --alloc ./alloc.json \
>   --output ./genesis.updated.json \
>   --stats --progress
> ```
>
> Validate the result with `jq empty genesis.updated.json` before booting anything from it. See
> [../README.md](../README.md#known-limitations) for the allocation format it accepts.

### Step 7. Generate the challenger prestates

Prestates are derived from the **final** `genesis.json` and `rollup.json`, so they must be
regenerated after Step 6.

```bash
git clone https://github.com/ethereum-optimism/optimism.git
cd optimism
git checkout op-program/v1.9.0
git submodule update --init --recursive

cp $WORKDIR/rollup.json  op-program/chainconfig/configs/${L2_CHAIN_ID}-rollup.json
cp $WORKDIR/genesis.json op-program/chainconfig/configs/${L2_CHAIN_ID}-genesis-l2.json

make reproducible-prestate
```

The command prints the Cannon64 absolute prestate hash. Rename the preimage file to that hash and
place both artefacts in the environment prestate directory:

```bash
cd op-program/bin
mv prestate-mt64.bin.gz 0x<CANNON64_PRESTATE_HASH>.bin.gz
cp 0x<CANNON64_PRESTATE_HASH>.bin.gz prestate-proof-mt64.json <ROLLUP_REPO>/envs/<env>/prestates/
```

- [ ] The challenger mount paths reference the new `0x<hash>.bin.gz` filename.
- [ ] The absolute prestate registered on L1 for the proposer game type matches the hash generated
      here. If the L1 dispute game implementation was deployed against a different prestate, a new
      game implementation has to be registered before withdrawals can be proven.

### Step 8. Boot the L2 with the new genesis

1. Place the artefacts where the stack expects them:

   ```bash
   cp genesis.json <ROLLUP_REPO>/envs/<env>/.deployer/genesis.json
   cp rollup.json  <ROLLUP_REPO>/envs/<env>/.deployer/rollup.json
   ```
2. Fill `envs/<env>/.env`: `L1_RPC_URL`, `L1_BEACON_URL`, the operator private keys and
   `DISPUTE_GAME_FACTORY_ADDRESS` from `state.json`.
3. **Clear execution-client and node volumes from any previous run.** Booting on top of an old
   database is the most common cause of a genesis hash mismatch.

   ```bash
   make down ENV=<env>
   rm -rf ./data/op-reth-seq ./data/op-reth-rpc
   ```
4. Confirm the proposer game type resolves to a non-zero implementation:

   ```bash
   cast call <DISPUTE_GAME_FACTORY_PROXY> "gameImpls(uint32)(address)" 1 --rpc-url "$L1_RPC_URL"
   ```
5. Confirm the batcher has a data availability type set (`--data-availability-type=blobs`); without
   it the batcher will not start.
6. Start:

   ```bash
   make up-safe ENV=<env>
   ```

- [ ] Sequencer block 0 hash equals `genesis.l2.hash` from `rollup.json`:

      ```bash
      curl -s -X POST http://127.0.0.1:8545 -H "Content-Type: application/json" \
        -d '{"jsonrpc":"2.0","method":"eth_getBlockByNumber","params":["0x0",false],"id":1}' | jq -r .result.hash
      ```

---

## Phase 3 – After migration

Run in this order; each stage gates the next.

### Step 9. Health checks

```bash
# Sequencer produces blocks
curl -s -X POST http://127.0.0.1:8545 -H "Content-Type: application/json" \
  -d '{"jsonrpc":"2.0","method":"eth_blockNumber","params":[],"id":1}'

# Sequencer op-node sync status
curl -s http://127.0.0.1:9545 -H "Content-Type: application/json" \
  -d '{"jsonrpc":"2.0","method":"optimism_syncStatus","params":[],"id":1}' | jq

# RPC node op-node sync status
curl -s http://127.0.0.1:19545 -H "Content-Type: application/json" \
  -d '{"jsonrpc":"2.0","method":"optimism_syncStatus","params":[],"id":1}' | jq
```

- [ ] `eth_chainId` equals `$L2_CHAIN_ID`.
- [ ] Block number increases, block time matches the configured value.
- [ ] `unsafe_l2` and `safe_l2` both advance – `safe_l2` advancing confirms the batcher is posting.
- [ ] The RPC node has the sequencer as a peer and shares the latest block with it.
- [ ] Gas is paid in WBT.
- [ ] Proposer is publishing output roots and its wallet is funded.

### Step 10. State reconciliation

Full procedure in [post-migration-verification.md](post-migration-verification.md). In short:

```bash
cd staterecon && go build -o staterecon ./cmd/staterecon && go build -o tokenrecon ./cmd/tokenrecon

./staterecon --l1-rpc <L1_ARCHIVE_RPC> --l2-rpc <L2_RPC> \
  --l1-block <SNAPSHOT_BLOCK_HEX> --output-dir ./out --print-mismatches

./tokenrecon --l1-rpc <L1_ARCHIVE_RPC> --l2-rpc <L2_RPC> \
  --config config/config.yml --output-dir ./token_out
```

- [ ] `staterecon` reports zero `mismatch` and zero `error` rows.
- [ ] `tokenrecon` reports zero `mismatch` and zero `error` rows.
- [ ] `soul-verification` migration state diff passes.

### Step 11. Functional QA

Genesis validation, core chain operations, canonical bridge, explorer, staking and SoulDrop, DEX,
wallets, sequencer resilience and load testing – see
[post-migration-verification.md](post-migration-verification.md).

### Step 12. Sign-off

- [ ] Zero reconciliation discrepancies on accounts, tokens and NFTs.
- [ ] All P0 QA cases pass.
- [ ] Explorer indexes blocks, transactions, tokens and verified contracts.
- [ ] Monitoring and alerting live for proposer interval, batcher submissions, sequencer health and
      any storage root mismatch.
- [ ] Migration record archived: snapshot block number, hash and state root, dump checksum,
      `alloc.json` checksum, final `genesis.json` and `rollup.json` checksums, L2 block 0 hash,
      prestate hash, tool versions.
