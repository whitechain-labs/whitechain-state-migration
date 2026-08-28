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
  for (const [addr, entry] of pendingBaseAlloc) writeAllocPair(addr, entry);

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
    `base genesis entries: ${baseAllocTotal} total, ${carriedOver} carried over, ` +
      `${replacedBaseEntries.length} replaced by dump accounts`,
  );

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
