#!/usr/bin/env node

/**
 * Insert alloc entries into a geth genesis config JSON.
 *
 * Behavior:
 * - Adds alloc accounts that are missing from `config.alloc`
 * - Does NOT overwrite existing `config.alloc[addr]` entries
 *
 * Input format note:
 * - For performance, the streaming parser expects the alloc JSON to be formatted like
 *   `scripts/dump-to-alloc.mjs` output: one alloc entry per line (`"0x<40 hex>":{...}`), wrapped in
 *   a bare `{` / `}` line. Any other line shape is a hard error: pretty-printed and single-line
 *   alloc files are rejected instead of being silently mis-parsed.
 *
 * Usage:
 *   node scripts/insert-alloc-into-config.mjs --config genesis.json --alloc alloc.json --output genesis.updated.json
 */
import fs from "node:fs";
import readline from "node:readline";
import path from "node:path";

// Set while an output file is being written, so a failed run does not leave a truncated
// (syntactically invalid) genesis file behind.
let partialOutputPath = undefined;

function parseArgs(argv) {
  const args = {
    config: undefined,
    alloc: undefined,
    output: undefined,
    pretty: false,
    stats: false,
    overwriteExisting: false,
    progress: false,
    progressIntervalMs: 5000,
  };

  for (let i = 0; i < argv.length; i++) {
    const a = argv[i];
    if (a === "--config") args.config = argv[++i];
    else if (a === "--alloc") args.alloc = argv[++i];
    else if (a === "--output") args.output = argv[++i];
    else if (a === "--pretty") args.pretty = true;
    else if (a === "--stats") args.stats = true;
    else if (a === "--overwrite-existing") args.overwriteExisting = true;
    else if (a === "--progress") args.progress = true;
    else if (a === "--progress-interval-ms") args.progressIntervalMs = Number(argv[++i]);
    else if (a === "--help" || a === "-h") return { ...args, help: true };
    else throw new Error(`Unknown arg: ${a}`);
  }

  return args;
}

const ADDR_KEY_RE = /^[0-9a-fA-F]{40}$/;

/**
 * Find the index just past the JSON value that starts at `start`.
 *
 * This is a structural scan, not a parser: it tracks string and bracket nesting only. It is enough
 * to prove that the value on this line is self-contained, which is what keeps the streamed output
 * syntactically valid. Returns -1 when the value is truncated or unbalanced — most importantly for
 * the `"0xaddr": {` header line of a pretty-printed alloc file.
 */
function scanJsonValueEnd(s, start) {
  let i = start;
  while (i < s.length && (s[i] === " " || s[i] === "\t")) i++;
  if (i >= s.length) return -1;

  let depth = 0;
  let inString = false;
  let escaped = false;

  for (; i < s.length; i++) {
    const c = s[i];

    if (inString) {
      if (escaped) escaped = false;
      else if (c === "\\") escaped = true;
      else if (c === '"') inString = false;
      continue;
    }

    if (c === '"') {
      inString = true;
      continue;
    }
    if (c === "{" || c === "[") {
      depth++;
      continue;
    }
    if (c === "}" || c === "]") {
      depth--;
      if (depth === 0) return i + 1;
      if (depth < 0) return -1;
      continue;
    }
    // A comma at depth 0 terminates a scalar value (`"addr":123,`).
    if (depth === 0 && c === ",") return i;
  }

  return depth === 0 && !inString ? s.length : -1;
}

async function main() {
  const args = parseArgs(process.argv.slice(2));
  if (args.help || !args.config || !args.alloc) {
    const msg = [
      "insert-alloc-into-config: merge alloc into genesis config (no overwrites)",
      "",
      "Usage:",
      "  node scripts/insert-alloc-into-config.mjs --config genesis.json --alloc alloc.json --output genesis.updated.json",
      "",
      "Options:",
      "  --config <path>    genesis config JSON",
      "  --alloc <path>     alloc JSON map, one entry per line (dump-to-alloc.mjs output)",
      "  --output <path>    write updated config (default: stdout)",
      "  --pretty            pretty-print output JSON (slower, larger)",
      "  --stats             show how many entries were added/skipped (slower)",
      "  --overwrite-existing overwrite existing config.alloc entries (faster)",
      "  --progress          print input-progress percent to stderr",
      "  --progress-interval-ms progress update interval (default: 5000)",
      "",
    ].join("\n");

    // eslint-disable-next-line no-console
    console.log(msg);
    if (!args.config || !args.alloc) process.exit(2);
    return;
  }

  const configPath = path.resolve(process.cwd(), args.config);
  const allocPath = path.resolve(process.cwd(), args.alloc);

  const configRaw = fs.readFileSync(configPath, "utf8");

  const config = JSON.parse(configRaw);

  if (!config || typeof config !== "object") {
    throw new Error("Invalid --config JSON (expected object)");
  }

  const configAlloc = config.alloc ?? {};
  if (typeof configAlloc !== "object" || Array.isArray(configAlloc)) {
    throw new Error("Invalid config.alloc (expected object map)");
  }
  if (!config.alloc) config.alloc = configAlloc;

  // Intentionally skip normalizing `config.alloc` here.
  // Duplicate detection in the streaming path does on-the-fly key normalization.

  // Stream the output and avoid building a gigantic `config.alloc` object. This is significantly
  // faster for millions of accounts and also reduces GC pressure.
  const outputPath = args.output ? path.resolve(process.cwd(), args.output) : undefined;
  const outStream = outputPath
    ? fs.createWriteStream(outputPath, { encoding: "utf8" })
    : process.stdout;
  partialOutputPath = outputPath;

  // Alloc keys are re-spelled in the same form as the existing `config.alloc` keys. Without
  // `--overwrite-existing` that is what makes duplicate detection work; with it, it is what makes
  // the duplicate key actually shadow the config entry ("last wins") instead of adding a second
  // spelling of the same address to the map.
  // Avoid building a Set of existing keys (can OOM for huge alloc maps): sample one key instead.
  // With an empty `config.alloc` there is no sample to copy, so keep the alloc file's own form
  // (`dump-to-alloc.mjs` writes `0x`-prefixed lowercase keys).
  let configKeyHas0x = true;
  let configKeyIsUppercase = false;
  {
    let sampleKey = undefined;
    for (const k in config.alloc) {
      if (Object.prototype.hasOwnProperty.call(config.alloc, k)) {
        sampleKey = k;
        break;
      }
    }
    if (typeof sampleKey === "string") {
      configKeyHas0x = sampleKey.startsWith("0x");
      // Detect A-F once from the sample key; we only care about whether it's mostly upper.
      for (let i = 2; i < sampleKey.length; i++) {
        const c = sampleKey.charCodeAt(i);
        if (c >= 65 && c <= 70) {
          configKeyIsUppercase = true;
          break;
        }
      }
    }
  }

  // Backpressure handling: pause readline while the output stream buffers.
  // Without this, huge alloc outputs (especially with `--pretty`) can OOM.
  let waitingDrain = false;
  let rl = undefined;
  const writeChunk = (chunk) => {
    if (chunk === "") return;
    const ok = outStream.write(chunk);
    if (!ok && !waitingDrain) {
      waitingDrain = true;
      if (rl) rl.pause();
      outStream.once("drain", () => {
        waitingDrain = false;
        if (rl) rl.resume();
      });
    }
  };

  // Everything except `alloc`, re-emitted without its closing brace so that the streamed alloc can
  // be appended. `head` is empty when `alloc` was the config's only key.
  const head = JSON.stringify(config, (k, v) => (k === "alloc" ? undefined : v)).slice(1, -1);
  writeChunk("{");
  if (head !== "") writeChunk(`${head},`);
  writeChunk(args.pretty ? '"alloc":{\n' : '"alloc":{');

  let firstAllocEntry = true;
  const writeAllocPair = (key, valueJson) => {
    const pair =
      !firstAllocEntry
        ? args.pretty
          ? `,\n  "${key}":${valueJson}`
          : `,"${key}":${valueJson}`
        : args.pretty
          ? `  "${key}":${valueJson}`
          : `"${key}":${valueJson}`;

    firstAllocEntry = false;
    writeChunk(pair);
  };

  // Output existing alloc first so keys missing from the alloc file remain present.
  // In overwrite mode, alloc entries will appear later (duplicate keys => "last wins").
  for (const k in config.alloc) {
    if (Object.prototype.hasOwnProperty.call(config.alloc, k)) {
      writeAllocPair(k, JSON.stringify(config.alloc[k]));
    }
  }

  const trackStats = args.stats;
  let added = trackStats ? 0 : undefined;
  let skipped = trackStats ? 0 : undefined;
  let entriesProcessed = 0;
  let lineNo = 0;

  const totalBytes = args.progress ? fs.statSync(allocPath).size : 0;
  let processedBytes = 0;
  let lastProgressPrintedAt = 0;
  let printed100 = false;
  let progressTimer = undefined;

  const allocInput = fs.createReadStream(allocPath, { encoding: "utf8" });
  if (args.progress) {
    allocInput.on("data", (chunk) => {
      processedBytes += chunk.length;
      const now = Date.now();
      if (now - lastProgressPrintedAt >= args.progressIntervalMs) {
        lastProgressPrintedAt = now;
        const pct = totalBytes > 0 ? Math.min(100, (processedBytes / totalBytes) * 100) : 0;
        if (!printed100) {
          // eslint-disable-next-line no-console
          console.error(`insert-alloc-into-config: progress=${pct.toFixed(2)}% entriesProcessed=${entriesProcessed}`);
        }
        if (pct >= 100) printed100 = true;
      }
    });

    const intervalMs = Number.isFinite(args.progressIntervalMs) ? args.progressIntervalMs : 5000;
    progressTimer = setInterval(() => {
      const pct = totalBytes > 0 ? Math.min(100, (processedBytes / totalBytes) * 100) : 0;
      if (!printed100) {
        // eslint-disable-next-line no-console
        console.error(`insert-alloc-into-config: progress=${pct.toFixed(2)}% entriesProcessed=${entriesProcessed}`);
      }
      if (pct >= 100) printed100 = true;
    }, intervalMs);
  }

  rl = readline.createInterface({ input: allocInput, crlfDelay: Infinity });

  await new Promise((resolve, reject) => {
    let failed = false;

    const fail = (reason, line) => {
      if (failed) return;
      failed = true;
      rl.close();
      const preview = line.length > 120 ? `${line.slice(0, 120)}...` : line;
      reject(
        new Error(
          [
            `Malformed alloc file at line ${lineNo}: ${reason}`,
            `  ${preview}`,
            "",
            'Expected one alloc entry per line ("0x<40 hex>":{...}), as written by',
            "scripts/dump-to-alloc.mjs. Pretty-printed alloc files and alloc maps written on a",
            "single line are not supported; regenerate alloc.json with dump-to-alloc.mjs, or",
            "reformat the existing file to one entry per line.",
          ].join("\n"),
        ),
      );
    };

    rl.on("line", (line) => {
      if (failed) return;
      lineNo++;

      const trimmed = line.trim();
      // Structural lines of the surrounding object.
      if (trimmed === "" || trimmed === "{" || trimmed === "}") return;

      // `{"0x..":{...}` — the first entry may share its line with the opening brace.
      let s = trimmed[0] === "{" && trimmed[1] === '"' ? trimmed.slice(1).trim() : trimmed;

      if (s[0] !== '"') {
        fail("expected an entry starting with a quoted address key", line);
        return;
      }

      const endQuote = s.indexOf('"', 1);
      if (endQuote < 0) {
        fail("unterminated address key", line);
        return;
      }
      const keyRaw = s.slice(1, endQuote);

      const colonAt = s.indexOf(":", endQuote + 1);
      if (colonAt < 0) {
        fail("missing `:` after the address key", line);
        return;
      }

      const valueEnd = scanJsonValueEnd(s, colonAt + 1);
      if (valueEnd < 0) {
        fail(`entry value for "${keyRaw}" is not a complete JSON value on this line`, line);
        return;
      }
      const valueJson = s.slice(colonAt + 1, valueEnd).trim();
      if (valueJson === "") {
        fail(`entry "${keyRaw}" has no value`, line);
        return;
      }

      // Only the entry separator and the closing brace of the alloc object may follow.
      const rest = s.slice(valueEnd).trim();
      if (rest !== "" && rest !== "," && rest !== "}" && rest !== "},") {
        fail(`unexpected trailing content after the entry for "${keyRaw}"`, line);
        return;
      }

      // Normalize key: accept both `0x...` and `...` (we expect 20-byte keys).
      const addrHex = keyRaw.startsWith("0x") || keyRaw.startsWith("0X") ? keyRaw.slice(2) : keyRaw;
      if (!ADDR_KEY_RE.test(addrHex)) {
        fail(`alloc key must be 20-byte hex (40 chars), got "${keyRaw}"`, line);
        return;
      }
      const addrKey = addrHex.toLowerCase();

      entriesProcessed++;

      // Spell the key the way `config.alloc` spells its own keys (the `0x` prefix is never
      // upper-cased).
      const hex = configKeyIsUppercase ? addrKey.toUpperCase() : addrKey;
      const outKey = configKeyHas0x ? `0x${hex}` : hex;

      if (!args.overwriteExisting) {
        if (Object.prototype.hasOwnProperty.call(config.alloc, outKey)) {
          if (trackStats) skipped++;
          return;
        }
        if (trackStats) added++;
      }

      // Overwrite semantics: the alloc entry is written after the config entry with the identical
      // key, so it shadows it ("last wins").
      writeAllocPair(outKey, valueJson);
    });

    rl.on("close", () => {
      if (!failed) resolve();
    });
    rl.on("error", (e) => {
      if (failed) return;
      failed = true;
      reject(e);
    });
  }).finally(() => {
    if (progressTimer) clearInterval(progressTimer);
  });

  if (args.progress && !printed100) {
    // eslint-disable-next-line no-console
    console.error(`insert-alloc-into-config: progress=100.00% entriesProcessed=${entriesProcessed}`);
  }

  // Close `alloc` object and then the outer config object.
  if (args.pretty) writeChunk("\n}\n}\n");
  else writeChunk("\n}}\n");

  if (outStream !== process.stdout) {
    await new Promise((resolve, reject) => {
      outStream.on("finish", resolve);
      outStream.on("error", reject);
      outStream.end();
    });
  }

  partialOutputPath = undefined;

  if (trackStats) {
    // eslint-disable-next-line no-console
    console.error(`insert-alloc-into-config: added=${added} skipped(existing)=${skipped}`);
  }
}

main().catch((e) => {
  if (partialOutputPath) {
    // A partially written config is invalid JSON; do not leave it lying around.
    try {
      fs.unlinkSync(partialOutputPath);
    } catch {
      /* ignore */
    }
  }
  // eslint-disable-next-line no-console
  console.error(e && e.message ? e.message : e);
  process.exit(1);
});
