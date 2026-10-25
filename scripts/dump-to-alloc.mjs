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
 * - An address defined by both the base genesis allocation and the dump is a collision.
 * - A base entry with code or storage is protected: an OP Stack predeploy proxy, implementation or
 *   preinstall. Anyone can fund such an address on L1 for 1 wei, so an L1 account never replaces it.
 *   The base code, storage and nonce are kept and the L1 balance is added to the base balance, so
 *   the native supply is unchanged. An L1 account there with different code, with storage, or with
 *   a nonce but no code cannot be merged safely and fails the run.
 * - A base entry with neither code nor storage (op-deployer's 1-wei entries at 0x00..00-0x00..ff)
 *   is replaced by the dump account.
 * - Every collision is reported on stderr, naming what was there and what replaced it, and listed
 *   again in a summary at the end of the run. The exception is a dump account that is empty and
 *   gets pruned: the base entry survives, and that outcome is reported too.
 * - Before the run succeeds, every protected base address is checked to have been written exactly
 *   once with its base code.
 * - A run without collisions says so explicitly, so silence is never ambiguous.
 * - Reports go to stderr, so they stay out of the allocation even when it is streamed to stdout.
 *
 * Records without an address:
 * - geth's iterative dump never writes `address` for the zero address (it cannot tell it apart from
 *   a missing preimage), only its secure trie `key`. Such a record is restored to
 *   0x0000000000000000000000000000000000000000 and goes through the same collision handling.
 * - Any other account record without an address is a missing preimage. It cannot be migrated, so the
 *   run fails instead of silently dropping the account and its balance.
 *
 * Usage:
 *   node scripts/dump-to-alloc.mjs --genesis genesis.json --input state.jsonl --output alloc.json
 *
 * Options:
 *   --genesis <path>       (required) genesis json file
 *   --input <path>         (required) dump json file
 *   --output <path>        write alloc json to file (default: stdout)
 *   --no-prune-empty       keep accounts with empty balance/nonce/code/storage
 */
import fs from "node:fs";
import readline from "node:readline";
import path from "node:path";

// Set while an output file is being written, so a failed run does not leave a truncated
// (syntactically invalid) alloc file behind.
let partialOutputPath = undefined;

function parseArgs(argv) {
  const args = {
    genesis: undefined,
    input: undefined,
    output: undefined,
    pruneEmpty: true,
  };

  for (let i = 0; i < argv.length; i++) {
    const a = argv[i];
    if (a === "--genesis") args.genesis = argv[++i];
    else if (a === "--input") args.input = argv[++i];
    else if (a === "--output") args.output = argv[++i];
    else if (a === "--no-prune-empty") args.pruneEmpty = false;
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

// keccak256(0x0000000000000000000000000000000000000000): the secure trie key geth writes for the
// zero address, whose dump record carries no `address` field.
const ZERO_ADDRESS = "0x0000000000000000000000000000000000000000";
const ZERO_ADDRESS_TRIE_KEY = "0x5380c7b7ae81a58eb98d9c78de4a1fd7fd9535fc953ed2be602daaa41767312a";

// Fields only an account record carries. The `{ "root": ... }` header has none of them; `root` alone
// does not tell the two apart, since account records carry their storage root under the same name.
const ACCOUNT_FIELDS = ["address", "key", "balance", "nonce", "codeHash", "code", "storage"];

function isAccountRecord(obj) {
  return ACCOUNT_FIELDS.some((f) => Object.prototype.hasOwnProperty.call(obj, f));
}

/**
 * Address of a dump account record. Records without `address` are resolved through their trie key;
 * anything that cannot be resolved fails the run, so no account is ever dropped silently.
 */
function resolveRecordAddress(obj) {
  if (typeof obj.address === "string") return normalizeAddress(obj.address);
  if (obj.address !== undefined && obj.address !== null) {
    throw new Error(`Invalid address field: ${JSON.stringify(obj.address)}`);
  }
  const key = typeof obj.key === "string" ? obj.key.toLowerCase() : undefined;
  if (key === ZERO_ADDRESS_TRIE_KEY) return ZERO_ADDRESS;
  throw new Error(
    `Account record without an address (trie key ${key ?? "<none>"}): missing preimage. ` +
      "Re-run the dump with --cache.preimages on a node that recorded them",
  );
}

/**
 * True for a base entry the OP Stack owns: one with code or storage. Base entries are not trusted
 * to spell fields consistently, so anything unreadable counts as protected.
 */
function isProtectedBaseEntry(entry) {
  if (!entry || typeof entry !== "object") return false;
  try {
    if (normalizeBytesHex(entry.code) !== "") return true;
  } catch {
    return true;
  }
  return !!entry.storage && typeof entry.storage === "object" && Object.keys(entry.storage).length > 0;
}

/**
 * Merge an L1 dump account into the protected base entry at the same address: the base contract is
 * kept and the L1 balance added to it. Throws when the L1 account cannot be merged safely.
 */
function mergeIntoProtectedEntry(addr, baseEntry, dumpEntry) {
  const baseCode = normalizeBytesHex(baseEntry.code);
  const dumpCode = normalizeBytesHex(dumpEntry.code);
  if (dumpCode !== "" && dumpCode !== baseCode) {
    throw new Error(
      `L1 account at protected base address ${addr} has different code than the base genesis; ` +
        "resolve it by hand before migrating",
    );
  }
  if (dumpEntry.storage) {
    throw new Error(
      `L1 account at protected base address ${addr} has storage; resolve it by hand before migrating`,
    );
  }
  if (dumpCode === "" && !isZeroQuantity(dumpEntry.nonce)) {
    throw new Error(
      `L1 account at protected base address ${addr} has nonce ${dumpEntry.nonce} but no code; ` +
        "resolve it by hand before migrating",
    );
  }
  const balance = BigInt(toHexQuantity(baseEntry.balance ?? "0")) + BigInt(dumpEntry.balance);
  return { ...baseEntry, balance: bigintToHexQuantity(balance) };
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
  const protectedMerges = [];

  // Code of every protected base entry, checked off as the entry is written. Anything left at the
  // end, or written twice, or written with other code, fails the run.
  const protectedBaseCode = new Map();
  for (const [addr, entry] of pendingBaseAlloc) {
    if (isProtectedBaseEntry(entry)) protectedBaseCode.set(addr, normalizeBytesHex(entry.code));
  }
  const protectedWritten = new Set();

  // Dump accounts read and pruned, so the entry count can be checked against the run itself.
  let dumpAccounts = 0;
  let prunedAccounts = 0;
  let zeroAddressRecovered = false;

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
    if (protectedBaseCode.has(addr)) {
      if (protectedWritten.has(addr)) {
        report(`COLLISION ${addr} - REJECTED, a second dump record for a protected base genesis contract`);
        reportDetail(`dump: ${describeAllocEntry(entry)}`);
        throw new Error(`Protected base address ${addr} would be written twice`);
      }
      if (normalizeBytesHex(entry.code) !== protectedBaseCode.get(addr)) {
        report(`COLLISION ${addr} - REJECTED, the base genesis contract would lose its code`);
        reportDetail(`written: ${describeAllocEntry(entry)}`);
        throw new Error(`Protected base address ${addr} would lose its base genesis code`);
      }
      protectedWritten.add(addr);
    }
    writeChunk(`${firstEntry ? "" : ",\n"}"${addr}":${JSON.stringify(entry)}`);
    firstEntry = false;
  };

  writeChunk("{\n");

  let lineNo = 0;
  const writeAccount = (obj) => {
    if (!obj || typeof obj !== "object") return;
    if (!isAccountRecord(obj)) return; // header/meta records

    const addr = resolveRecordAddress(obj);
    if (typeof obj.address !== "string") {
      zeroAddressRecovered = true;
      report(`ZERO ADDRESS ${addr} - restored from a dump record without an address field (trie key)`);
    }
    dumpAccounts++;
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
      prunedAccounts++;
      if (baseEntry !== undefined) {
        keptBaseEntries.push(addr);
        report(`COLLISION ${addr} - keeping the base genesis entry`);
        reportDetail("the dump account is empty and was pruned, so the base entry survives");
        reportDetail(`base: ${describeAllocEntry(baseEntry)}`);
      }
      return;
    }

    // A protected base entry (predeploy, implementation, preinstall) keeps its contract; only the L1
    // balance is carried over.
    if (baseEntry !== undefined && protectedBaseCode.has(addr)) {
      let merged;
      try {
        merged = mergeIntoProtectedEntry(addr, baseEntry, allocEntry);
      } catch (e) {
        // Logged like every other collision, so the failing one is not only in the error message.
        report(`COLLISION ${addr} - REJECTED, the L1 account cannot be merged into the base genesis contract`);
        reportDetail(`base: ${describeAllocEntry(baseEntry)}`);
        reportDetail(`dump: ${describeAllocEntry(allocEntry)}`);
        throw e;
      }
      protectedMerges.push(addr);
      report(`COLLISION ${addr} - PROTECTED, keeping the base genesis contract, carrying over the L1 balance`);
      reportDetail(`base: ${describeAllocEntry(baseEntry)}`);
      reportDetail(`dump: ${describeAllocEntry(allocEntry)}`);
      reportDetail(`result: ${describeAllocEntry(merged)}`);
      pendingBaseAlloc.delete(addr);
      writeAllocPair(addr, merged);
      return;
    }

    // A dump account replaces a base entry that holds no contract (balance only).
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
  for (const [addr, entry] of pendingBaseAlloc) writeAllocPair(addr, entry);

  for (const addr of protectedBaseCode.keys()) {
    if (!protectedWritten.has(addr)) throw new Error(`Protected base address ${addr} was not written`);
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
  const carriedOver = pendingBaseAlloc.size;
  report(
    `dump accounts: ${dumpAccounts} read, ${prunedAccounts} pruned as empty, ` +
      `${dumpAccounts - prunedAccounts} written` +
      (zeroAddressRecovered ? " (including the zero address restored from its trie key)" : ""),
  );
  report(
    `base genesis entries: ${baseAllocTotal} total, ${carriedOver} carried over, ` +
      `${replacedBaseEntries.length} replaced by dump accounts, ` +
      `${protectedMerges.length} protected contract${protectedMerges.length === 1 ? "" : "s"} merged with an L1 balance, ` +
      `${protectedBaseCode.size} protected contract${protectedBaseCode.size === 1 ? "" : "s"} verified`,
  );

  if (replacedBaseEntries.length > 0) {
    report(`REPLACED ${replacedBaseEntries.length} base genesis entr${replacedBaseEntries.length === 1 ? "y" : "ies"}:`);
    for (const addr of replacedBaseEntries) reportDetail(addr);
    report("review every one before booting: these base entries held no contract, only a balance");
  }

  if (protectedMerges.length > 0) {
    report(
      `PROTECTED ${protectedMerges.length} base genesis contract${protectedMerges.length === 1 ? "" : "s"} kept, L1 balance carried over:`,
    );
    for (const addr of protectedMerges) reportDetail(addr);
  }

  if (keptBaseEntries.length > 0) {
    report(
      `kept ${keptBaseEntries.length} base genesis entr${keptBaseEntries.length === 1 ? "y" : "ies"} over an empty pruned dump account:`,
    );
    for (const addr of keptBaseEntries) reportDetail(addr);
  }

  if (replacedBaseEntries.length === 0 && keptBaseEntries.length === 0 && protectedMerges.length === 0) {
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
