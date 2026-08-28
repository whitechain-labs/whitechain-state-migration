import { assertMigrationStateMatches, MigrationCheckConfig, normalizeSampleSoulIds } from "../migrationStateSnapshot";
import { loadEnv } from "../env";

function mustEnv(name: string): string {
  const v = process.env[name];
  if (!v || typeof v !== "string") throw new Error(`Missing env var ${name}`);
  return v;
}

function hasRequiredEnv(names: string[]): boolean {
  return names.every((n) => typeof process.env[n] === "string" && process.env[n]?.length > 0);
}

function maybeEnvNumber(name: string): number | undefined {
  const v = process.env[name];
  if (!v) return undefined;
  const n = Number(v);
  return Number.isFinite(n) ? n : undefined;
}

function parseBool(name: string, defaultValue: boolean): boolean {
  const v = process.env[name];
  if (v == null) return defaultValue;
  return v === "1" || v.toLowerCase() === "true";
}

/**
 * Comma-separated soul IDs, e.g. "1,42,100". Whitespace is trimmed; duplicates removed.
 */
function parseSampleSoulIdsFromEnv(): string[] {
  const raw = process.env.MIGRATION_SAMPLE_SOUL_IDS;
  if (!raw || typeof raw !== "string") {
    throw new Error("MIGRATION_SAMPLE_SOUL_IDS is required for the sample migration diff test (comma-separated soul IDs, e.g. 1,2,5)");
  }
  const parts = raw.split(",").map((s) => s.trim()).filter(Boolean);
  const ids = normalizeSampleSoulIds(parts);
  if (ids.length === 0) {
    throw new Error("MIGRATION_SAMPLE_SOUL_IDS resolved to no valid soul IDs");
  }
  return ids;
}

describe("RPC migration state diff (sampled souls)", () => {
  const required = [
    "ORIGINAL_RPC_URL",
    "TARGET_RPC_URL",
    "SOUL_REGISTRY_CONFIG_ORIGINAL",
    "SOUL_REGISTRY_CONFIG_TARGET",
    "SOUL_REGISTRY_ORIGINAL",
    "SOUL_REGISTRY_TARGET",
    "SOUL_ATTRIBUTE_REGISTRY_ORIGINAL",
    "SOUL_ATTRIBUTE_REGISTRY_TARGET",
    "SOUL_BOUND_TOKEN_REGISTRY_ORIGINAL",
    "SOUL_BOUND_TOKEN_REGISTRY_TARGET",
    "HOLD_AMOUNT_ORIGINAL",
    "HOLD_AMOUNT_TARGET",
    "IS_VERIFIED_ORIGINAL",
    "IS_VERIFIED_TARGET",
    "SOUL_DROP_ORIGINAL",
    "SOUL_DROP_TARGET",
  ];

  it("should match migrated contract state for sampled soul IDs (spot-check)", async function () {
    loadEnv();
    if (!hasRequiredEnv(required)) {
      throw new Error(`Missing required ENV vars: ${required.join(", ")}`);
    }

    this.timeout(3000000);

    const sampleSoulIds = parseSampleSoulIdsFromEnv();

    // eslint-disable-next-line no-console
    console.log(
      `[migration sample test] spot-checking soulIds=${sampleSoulIds.join(",")} ` +
        "(global contract fields are still compared in full; per-soul data is subset only)"
    );

    const config: MigrationCheckConfig = {
      originalRpcUrl: mustEnv("ORIGINAL_RPC_URL"),
      targetRpcUrl: mustEnv("TARGET_RPC_URL"),
      soulRegistryConfig: {
        original: mustEnv("SOUL_REGISTRY_CONFIG_ORIGINAL"),
        target: mustEnv("SOUL_REGISTRY_CONFIG_TARGET"),
      },
      soulRegistry: {
        original: mustEnv("SOUL_REGISTRY_ORIGINAL"),
        target: mustEnv("SOUL_REGISTRY_TARGET"),
      },
      soulAttributeRegistry: {
        original: mustEnv("SOUL_ATTRIBUTE_REGISTRY_ORIGINAL"),
        target: mustEnv("SOUL_ATTRIBUTE_REGISTRY_TARGET"),
      },
      soulBoundTokenRegistry: {
        original: mustEnv("SOUL_BOUND_TOKEN_REGISTRY_ORIGINAL"),
        target: mustEnv("SOUL_BOUND_TOKEN_REGISTRY_TARGET"),
      },
      holdAmount: {
        original: mustEnv("HOLD_AMOUNT_ORIGINAL"),
        target: mustEnv("HOLD_AMOUNT_TARGET"),
      },
      isVerified: {
        original: mustEnv("IS_VERIFIED_ORIGINAL"),
        target: mustEnv("IS_VERIFIED_TARGET"),
      },
      soulDrop: {
        original: mustEnv("SOUL_DROP_ORIGINAL"),
        target: mustEnv("SOUL_DROP_TARGET"),
      },
      sampleSoulIds,
      maxHoldLevelKey: maybeEnvNumber("MIGRATION_MAX_HOLD_LEVEL_KEY"),
      enableStorageLayoutChecks: parseBool("MIGRATION_ENABLE_STORAGE_LAYOUT_CHECKS", true),
      enableReverseIndexChecks: parseBool("MIGRATION_ENABLE_REVERSE_INDEX_CHECKS", false),
      originalBlockNumber: maybeEnvNumber("ORIGINAL_BLOCK_NUMBER"),
      targetBlockNumber: maybeEnvNumber("TARGET_BLOCK_NUMBER"),
    };

    const earlybirdOriginal = process.env.EARLYBIRD_ORIGINAL;
    const earlybirdTarget = process.env.EARLYBIRD_TARGET;
    if (earlybirdOriginal && earlybirdTarget) {
      config.knownTokenCollections = [
        {
          label: "EarlyBird",
          originalAddress: earlybirdOriginal,
          targetAddress: earlybirdTarget,
        },
      ];
    }

    await assertMigrationStateMatches(config);

    // eslint-disable-next-line no-console
    console.log("[migration sample test] RPC migration state diff (sampled): OK");
  });
});
