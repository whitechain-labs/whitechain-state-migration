#!/usr/bin/env node

/**
 * Convert a JSONL dump of EVM account data into a geth genesis `alloc` map.
 *
 * Input shape (JSON Lines):
 * - Each line is a JSON object.
 * - The first line is the header (e.g. `{ "root": "0x..." }`); it is ignored.
 * - Account records look like:
 *   {
 *     "address": "0x47fD...200f",
 *     "balance": "5000000000000",
 *     "nonce": 0,
 *     "code": "0x...",          // optional
 *     "storage": {              // optional
 *       "0x000...02": "024a...",
 *       "0x000...03": "7856..."
 *     }
 *   }
 *
 * Output shape:
 * - A single JSON object, written as a stream, with exactly one alloc entry per line:
 *
 *     {
 *     "0x<40 hex>":{"balance":"0x...","nonce":"0x0"},
 *     "0x<40 hex>":{"balance":"0x0","nonce":"0x1","code":"0x60..."}
 *     }
 *
 * - The document is valid JSON (consumable by `jq` and `op-node genesis l2 --l2-allocs`) and is
 *   also directly consumable by `scripts/insert-alloc-into-config.mjs`, whose parser reads one
 *   entry per line.
 * - Nothing but the base genesis allocation is held in memory, so the peak footprint does not grow
 *   with the size of the dump.
 *
 * Address collisions:
 * - An address defined by both the base genesis allocation and the dump is a collision. The dump
 *   account wins, which for an OP Stack predeploy means its code and storage are dropped in favour
 *   of the L1 account.
 * - Every collision is reported on stderr, naming what was there and what replaced it, and listed
 *   again in a summary at the end of the run. The exception is a dump account that is empty and
 *   gets pruned: the base entry survives, and that outcome is reported too.
 * - A run without collisions says so explicitly, so silence is never ambiguous.
 * - Reports go to stderr, so they stay out of the allocation even when it is streamed to stdout.
 *
 * Hardhat dev accounts:
 * - The 30 accounts derived from the hardhat/foundry test mnemonic are dropped by default, from the
 *   dump and from the base genesis allocation alike, because `op-node genesis l2` refuses to build a
 *   genesis whose allocation contains any of them while the deploy config has
 *   `fundDevAccounts = false`. Presence is what it checks, not funding: the zero-balance,
 *   nonce-1 account a devnet leaves behind on L1 is enough to fail the run.
 * - Every drop is reported, because it means the L1 balance and nonce of that account are not
 *   carried to L2. `--keep-dev-accounts` keeps them, and then the deploy config must set
 *   `fundDevAccounts = true`.
 *
 * Usage:
 *   node scripts/dump-to-alloc.mjs --genesis genesis.json --input state.jsonl --output alloc.json
 *
 * Options:
 *   --genesis <path>       (required) genesis json file
 *   --input <path>         (required) dump json file
 *   --output <path>        write alloc json to file (default: stdout)
 *   --no-prune-empty       keep accounts with empty balance/nonce/code/storage
 *   --keep-dev-accounts    keep the hardhat dev accounts instead of dropping them
 */
import fs from "node:fs";
import readline from "node:readline";
import path from "node:path";

/**
 * The 30 accounts derived from the hardhat/foundry test mnemonic
 * ("test test test test test test test test test test test junk") at m/44'/60'/0'/0/0 .. /29.
 *
 * `op-node genesis l2` derives this exact set (`RetrieveDevAccounts` in op-chain-ops/genesis) and
 * aborts with "deploy config mismatch with allocs" when the allocation holds any of them and the
 * deploy config has `fundDevAccounts = false`.
 *
 * Deliberately absent, although upstream funds them alongside the dev accounts: the geth test
 * account `0x71562b71999873db5b286df957af199ec94617f7` and the deterministic-deployment proxy
 * deployer `0x3fab184622dc19b6109349b94811493bf2a45362`. The genesis check ignores both, and the
 * create2 deployer is a real address that can hold real L1 state.
 */
const DEV_ACCOUNTS = new Set([
  // m/44'/60'/0'/0/0 .. /19 - the accounts hardhat and anvil print on start
  "0xf39fd6e51aad88f6f4ce6ab8827279cfffb92266",
  "0x70997970c51812dc3a010c7d01b50e0d17dc79c8",
  "0x3c44cdddb6a900fa2b585dd299e03d12fa4293bc",
  "0x90f79bf6eb2c4f870365e785982e1f101e93b906",
  "0x15d34aaf54267db7d7c367839aaf71a00a2c6a65",
  "0x9965507d1a55bcc2695c58ba16fb37d819b0a4dc",
  "0x976ea74026e726554db657fa54763abd0c3a0aa9",
  "0x14dc79964da2c08b23698b3d3cc7ca32193d9955",
  "0x23618e81e3f5cdf7f54c3d65f7fbc0abf5b21e8f",
  "0xa0ee7a142d267c1f36714e4a8f75612f20a79720",
  "0xbcd4042de499d14e55001ccbb24a551f3b954096",
  "0x71be63f3384f5fb98995898a86b02fb2426c5788",
  "0xfabb0ac9d68b0b445fb7357272ff202c5651694a",
  "0x1cbd3b2770909d4e10f157cabc84c7264073c9ec",
  "0xdf3e18d64bc6a983f673ab319ccae4f1a57c7097",
  "0xcd3b766ccdd6ae721141f452c550ca635964ce71",
  "0x2546bcd3c84621e976d8185a91a922ae77ecec30",
  "0xbda5747bfd65f08deb54cb465eb87d40e51b197e",
  "0xdd2fd4581271e230360230f9337d5c0430bf44c0",
  "0x8626f6940e2eb28930efb4cef49b2d1f2c9c1199",
  // m/44'/60'/0'/0/20 .. /29 - funded by L2Genesis.s.sol alongside the 20 above
  "0x09db0a93b389bef724429898f539aeb7ac2dd55f",
  "0x02484cb50aac86eae85610d6f4bf026f30f6627d",
  "0x08135da0a343e492fa2d4282f2ae34c6c5cc1bbe",
  "0x5e661b79fe2d3f6ce70f5aac07d8cd9abb2743f1",
  "0x61097ba76cd906d2ba4fd106e757f7eb455fc295",
  "0xdf37f81daad2b0327a0a50003740e1c935c70913",
  "0x553bc17a05702530097c3677091c5bb47a3a7931",
  "0x87bdce72c06c21cd96219bd8521bdf1f42c78b5e",
  "0x40fc963a729c542424cd800349a7e4ecc4896624",
  "0x9dcce783b6464611f38631e6c851bf441907c710",
]);

// Set while an output file is being written, so a failed run does not leave a truncated
// (syntactically invalid) alloc file behind.
let partialOutputPath = undefined;

function parseArgs(argv) {
  const args = {
    genesis: undefined,
    input: undefined,
    output: undefined,
    pruneEmpty: true,
    keepDevAccounts: false,
  };

  for (let i = 0; i < argv.length; i++) {
    const a = argv[i];
    if (a === "--genesis") args.genesis = argv[++i];
    else if (a === "--input") args.input = argv[++i];
    else if (a === "--output") args.output = argv[++i];
    else if (a === "--no-prune-empty") args.pruneEmpty = false;
    else if (a === "--keep-dev-accounts") args.keepDevAccounts = true;
    else if (a === "--help" || a === "-h") {
      return { ...args, help: true };
    } else {
      throw new Error(`Unknown arg: ${a}`);
    }
  }
  return args;
}

function strip0x(s) {
  return typeof s === "string" && s.startsWith("0x") ? s.slice(2) : s;
}

function toHexQuantity(value) {
  if (value === null || value === undefined) return "0x0";
  if (typeof value === "string") {
    if (value.startsWith("0x")) {
      const v = value.toLowerCase();
      return v === "0x" ? "0x0" : v;
    }
    if (value.trim() === "") return "0x0";
    // decimal string
    return bigintToHexQuantity(BigInt(value));
  }
  if (typeof value === "number") return bigintToHexQuantity(BigInt(value));
  if (typeof value === "bigint") return bigintToHexQuantity(value);
  throw new Error(`Unsupported quantity type: ${typeof value}`);
}

function bigintToHexQuantity(n) {
  if (n === 0n) return "0x0";
  if (n < 0n) throw new Error("Negative quantities not supported");
  return `0x${n.toString(16)}`;
}

function normalizeAddress(addr) {
  if (typeof addr !== "string") {
    throw new Error(`Invalid address key: ${addr}`);
  }
  const h = strip0x(addr).toLowerCase();
  if (h.length !== 40) throw new Error(`Address not 20 bytes: ${addr}`);
  if (!/^[0-9a-f]+$/.test(h)) throw new Error(`Address is not hex: ${addr}`);
  return `0x${h}`;
}

function normalizeBytesHex(s) {
  if (s === null || s === undefined) return "";
  if (typeof s !== "string") throw new Error(`Expected hex string, got: ${typeof s}`);
  const h = strip0x(s).toLowerCase();
  if (h === "") return "";
  if (!/^[0-9a-f]+$/.test(h)) throw new Error(`Non-hex value: ${s}`);
  return h.length % 2 === 0 ? h : `0${h}`;
}

function padTo32Bytes(hexNo0x) {
  const h = normalizeBytesHex(hexNo0x);
  if (h.length > 64) throw new Error(`Value exceeds 32 bytes: 0x${h}`);
  return `0x${h.padStart(64, "0")}`;
}

function padSlotKeyTo32Bytes(slotKey) {
  if (typeof slotKey !== "string") throw new Error(`Invalid storage key: ${slotKey}`);
  const h = normalizeBytesHex(slotKey);
  if (h.length > 64) throw new Error(`Storage key exceeds 32 bytes: ${slotKey}`);
  return `0x${h.padStart(64, "0")}`;
}

function isZeroQuantity(q) {
  return q === "0x0" || q === "0x00";
}

function isEmptyAllocEntry(entry) {
  const hasBalance = entry.balance && !isZeroQuantity(entry.balance);
  const hasNonce = entry.nonce && !isZeroQuantity(entry.nonce);
  const hasCode = entry.code && entry.code !== "0x";
  const hasStorage = entry.storage && Object.keys(entry.storage).length > 0;
  return !(hasBalance || hasNonce || hasCode || hasStorage);
}

/**
 * One-line summary of an alloc entry for the collision report.
 *
 * Entries come either from this script's own conversion or straight out of the base genesis file,
 * where field spellings are whatever the genesis producer wrote. Reporting must never be the thing
 * that fails a migration run, so anything unreadable degrades to a placeholder.
 */
function describeAllocEntry(entry) {
  if (!entry || typeof entry !== "object") return "<no entry>";

  const quantity = (value) => {
    try {
      return toHexQuantity(value ?? "0");
    } catch {
      return "<unreadable>";
    }
  };

  const codeHex = typeof entry.code === "string" ? strip0x(entry.code) : "";
  const codeBytes = Math.floor(codeHex.length / 2);
  const slots =
    entry.storage && typeof entry.storage === "object" ? Object.keys(entry.storage).length : 0;

  return [
    `balance=${quantity(entry.balance)}`,
    `nonce=${quantity(entry.nonce)}`,
    codeBytes > 0 ? `code=${codeBytes} bytes` : "code=none",
    slots > 0 ? `storage=${slots} slot(s)` : "storage=none",
  ].join(" ");
}

function report(line) {
  process.stderr.write(`dump-to-alloc: ${line}\n`);
}

function reportDetail(line) {
  process.stderr.write(`dump-to-alloc:     ${line}\n`);
}

/**
 * Load the base allocation from the genesis file into a Map keyed by normalized address.
 * This is the OP Stack predeploy set, so it is small enough to keep in memory.
 */
function loadGenesisAlloc(genesisPath) {
  const genesisRaw = fs.readFileSync(genesisPath, "utf8");
  const genesis = JSON.parse(genesisRaw);
  const sourceAlloc = genesis.alloc ?? genesis.allocs;

  if (!sourceAlloc || typeof sourceAlloc !== "object" || Array.isArray(sourceAlloc)) {
    throw new Error("Genesis file must contain an `alloc` object");
  }

  const normalizedAlloc = new Map();
  for (const [address, entry] of Object.entries(sourceAlloc)) {
    normalizedAlloc.set(normalizeAddress(address), entry);
  }

  return normalizedAlloc;
}

async function main() {
  const args = parseArgs(process.argv.slice(2));
  if (args.help || !args.genesis || !args.input) {
    const msg = [
      "dump-to-alloc: merge genesis `alloc` with JSONL account dump",
      "",
      "Usage:",
      "  node scripts/dump-to-alloc.mjs --genesis genesis.json --input state.jsonl --output alloc.json",
      "",
      "Options:",
      "  --genesis <path>       (required) genesis json file",
      "  --input <path>         (required) dump jsonl file",
      "  --output <path>        write alloc json to file (default: stdout)",
      "  --no-prune-empty       keep accounts with empty balance/nonce/code/storage",
      "  --keep-dev-accounts    keep the hardhat dev accounts (op-node then needs fundDevAccounts = true)",
      "",
      "Output is a JSON object with one alloc entry per line, streamed as the dump is read.",
      "",
    ].join("\n");
    if (!args.genesis || !args.input) {
      // eslint-disable-next-line no-console
      console.error(msg);
      process.exit(2);
    }
    // eslint-disable-next-line no-console
    console.log(msg);
    return;
  }

  const genesisPath = path.resolve(process.cwd(), args.genesis);
  const inputPath = path.resolve(process.cwd(), args.input);

  // Base allocation entries that no dump account has replaced yet. Entries are removed as the
  // dump overwrites them and whatever is left is appended after the dump has been streamed.
  const pendingBaseAlloc = loadGenesisAlloc(genesisPath);
  const baseAllocTotal = pendingBaseAlloc.size;

  // Addresses defined by both the base allocation and the dump. Bounded by the size of the base
  // allocation, so collecting every one of them costs nothing even on a full mainnet dump.
  const replacedBaseEntries = [];
  const keptBaseEntries = [];

  // Hardhat dev accounts dropped on the way out, by where they came from.
  const droppedDevDumpAccounts = [];
  const droppedDevBaseEntries = [];

  const outputPath = args.output ? path.resolve(process.cwd(), args.output) : undefined;
  const outStream = outputPath
    ? fs.createWriteStream(outputPath, { encoding: "utf8" })
    : process.stdout;
  partialOutputPath = outputPath;

  const rl = readline.createInterface({
    input: fs.createReadStream(inputPath, { encoding: "utf8" }),
    crlfDelay: Infinity,
  });

  // Backpressure handling: pause the reader while the output stream buffers. Without this a
  // multi-gigabyte allocation piles up in the write queue and the process runs out of memory.
  let waitingDrain = false;
  const writeChunk = (chunk) => {
    if (chunk === "") return;
    const ok = outStream.write(chunk);
    if (!ok && !waitingDrain) {
      waitingDrain = true;
      rl.pause();
      outStream.once("drain", () => {
        waitingDrain = false;
        rl.resume();
      });
    }
  };

  // One alloc entry per line. `scripts/insert-alloc-into-config.mjs` parses this shape directly,
  // and the document as a whole is still valid JSON.
  let firstEntry = true;
  const writeAllocPair = (addr, entry) => {
    writeChunk(`${firstEntry ? "" : ",\n"}"${addr}":${JSON.stringify(entry)}`);
    firstEntry = false;
  };

  writeChunk("{\n");

  let lineNo = 0;
  const writeAccount = (obj) => {
    if (!obj || typeof obj !== "object") return;
    if (typeof obj.address !== "string") return; // header/meta records may omit address

    const addr = normalizeAddress(obj.address);

    // A dev account is dropped whatever it holds, and its base genesis entry goes with it: leaving
    // either one in the allocation is what makes `op-node genesis l2` refuse to build the genesis.
    if (!args.keepDevAccounts && DEV_ACCOUNTS.has(addr)) {
      const shadowedBaseEntry = pendingBaseAlloc.get(addr);
      droppedDevDumpAccounts.push(addr);
      report(`DEV ACCOUNT ${addr} - dropping the L1 dump account`);
      reportDetail(`dump: ${describeAllocEntry(obj)}`);
      if (shadowedBaseEntry !== undefined) {
        droppedDevBaseEntries.push(addr);
        pendingBaseAlloc.delete(addr);
        reportDetail(`base genesis entry dropped as well: ${describeAllocEntry(shadowedBaseEntry)}`);
      }
      return;
    }

    const balance = toHexQuantity(obj.balance ?? "0");
    const nonce = toHexQuantity(obj.nonce ?? "0");

    let code = undefined;
    if (obj.code !== undefined && obj.code !== null) {
      if (typeof obj.code !== "string" || !obj.code.startsWith("0x")) {
        throw new Error(`Account ${addr} has non-hex code field`);
      }
      code = obj.code.toLowerCase();
    }

    let storage = undefined;
    if (obj.storage && typeof obj.storage === "object") {
      const s = {};
      for (const [k, v] of Object.entries(obj.storage)) {
        const key32 = padSlotKeyTo32Bytes(k);
        const val32 = padTo32Bytes(v);
        s[key32] = val32;
      }
      if (Object.keys(s).length > 0) storage = s;
    }

    const allocEntry = {
      balance,
      nonce,
      ...(code ? { code } : {}),
      ...(storage ? { storage } : {}),
    };

    const baseEntry = pendingBaseAlloc.get(addr);

    // A pruned dump account leaves the base allocation entry for that address untouched.
    if (args.pruneEmpty && isEmptyAllocEntry(allocEntry)) {
      if (baseEntry !== undefined) {
        keptBaseEntries.push(addr);
        report(`COLLISION ${addr} - keeping the base genesis entry`);
        reportDetail("the dump account is empty and was pruned, so the base entry survives");
        reportDetail(`base: ${describeAllocEntry(baseEntry)}`);
      }
      return;
    }

    // A dump account replaces the base allocation entry for the same address.
    if (baseEntry !== undefined) {
      replacedBaseEntries.push(addr);
      report(`COLLISION ${addr} - REPLACING the base genesis entry with the L1 dump account`);
      reportDetail(`base: ${describeAllocEntry(baseEntry)}`);
      reportDetail(`dump: ${describeAllocEntry(allocEntry)}`);
    }

    pendingBaseAlloc.delete(addr);
    writeAllocPair(addr, allocEntry);
  };

  await new Promise((resolve, reject) => {
    let failed = false;
    const fail = (err) => {
      if (failed) return;
      failed = true;
      rl.close();
      reject(err);
    };

    rl.on("line", (line) => {
      if (failed) return;
      lineNo++;
      const trimmed = line.trim();
      if (trimmed === "") return;

      let obj;
      try {
        obj = JSON.parse(trimmed);
      } catch (e) {
        fail(new Error(`Failed parsing JSONL at line ${lineNo}: ${(e && e.message) || e}`));
        return;
      }

      try {
        writeAccount(obj);
      } catch (e) {
        fail(
          new Error(
            `Failed converting JSONL account at line ${lineNo}: ${(e && e.message) || e}`,
          ),
        );
      }
    });

    rl.on("close", () => {
      if (!failed) resolve();
    });
    rl.on("error", fail);
  });

  // Base allocation entries the dump did not replace. The dump is streamed first, so these end up
  // at the tail; JSON object key order is not significant for geth or for op-node.
  let carriedOver = 0;
  for (const [addr, entry] of pendingBaseAlloc) {
    if (!args.keepDevAccounts && DEV_ACCOUNTS.has(addr)) {
      droppedDevBaseEntries.push(addr);
      report(`DEV ACCOUNT ${addr} - dropping the base genesis entry`);
      reportDetail(`base: ${describeAllocEntry(entry)}`);
      continue;
    }
    writeAllocPair(addr, entry);
    carriedOver++;
  }

  writeChunk("\n}\n");

  if (outStream !== process.stdout) {
    await new Promise((resolve, reject) => {
      outStream.on("finish", resolve);
      outStream.on("error", reject);
      outStream.end();
    });
  }

  partialOutputPath = undefined;

  // Summary last, so it is what an operator sees after a long run. Reported unconditionally: with
  // no collisions, silence would be indistinguishable from a run that never checked.
  report(
    `base genesis entries: ${baseAllocTotal} total, ${carriedOver} carried over, ` +
      `${replacedBaseEntries.length} replaced by dump accounts, ` +
      `${droppedDevBaseEntries.length} dropped as dev accounts`,
  );

  if (args.keepDevAccounts) {
    report(
      "dev-account filter disabled by --keep-dev-accounts: any hardhat dev account in the dump or " +
        "the base allocation is still there",
    );
    report("`op-node genesis l2` accepts that only with fundDevAccounts = true in the deploy config");
  } else if (droppedDevDumpAccounts.length > 0 || droppedDevBaseEntries.length > 0) {
    report(
      `dropped hardhat dev accounts: ${droppedDevDumpAccounts.length} from the dump, ` +
        `${droppedDevBaseEntries.length} from the base genesis allocation:`,
    );
    for (const addr of droppedDevDumpAccounts) reportDetail(`dump ${addr}`);
    for (const addr of droppedDevBaseEntries) reportDetail(`base ${addr}`);
    report(
      "their balance and nonce are not carried to L2; keeping them would make op-node reject the " +
        "allocation while the deploy config has fundDevAccounts = false",
    );
  } else {
    report("no hardhat dev accounts in the dump or in the base genesis allocation");
  }

  if (replacedBaseEntries.length > 0) {
    report(`REPLACED ${replacedBaseEntries.length} base genesis entr${replacedBaseEntries.length === 1 ? "y" : "ies"}:`);
    for (const addr of replacedBaseEntries) reportDetail(addr);
    report(
      "review every one before booting: an OP Stack predeploy replaced by an L1 account loses its code and storage",
    );
  }

  if (keptBaseEntries.length > 0) {
    report(
      `kept ${keptBaseEntries.length} base genesis entr${keptBaseEntries.length === 1 ? "y" : "ies"} over an empty pruned dump account:`,
    );
    for (const addr of keptBaseEntries) reportDetail(addr);
  }

  if (replacedBaseEntries.length === 0 && keptBaseEntries.length === 0) {
    report("no address collisions between the base genesis allocation and the dump");
  }
}

main().catch((e) => {
  if (partialOutputPath) {
    // A partially written alloc file is invalid JSON; do not leave it lying around.
    try {
      fs.unlinkSync(partialOutputPath);
    } catch {
      /* ignore */
    }
  }
  // eslint-disable-next-line no-console
  console.error(e);
  process.exit(1);
});
