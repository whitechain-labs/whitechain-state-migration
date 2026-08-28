import { assertMigrationStateMatches, MigrationCheckConfig } from "../migrationStateSnapshot";
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

describe("RPC migration state diff", () => {
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

  it("should match migrated contract state", async function () {
    loadEnv();
    if (!hasRequiredEnv(required)) {
      throw new Error(`Missing required ENV vars: ${required.join(", ")}`);
    }

    this.timeout(3000000);

    // eslint-disable-next-line no-console
    console.log("[migration test] starting RPC migration state diff");

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
      maxSoulIds: maybeEnvNumber("MIGRATION_MAX_SOUL_IDS"),
      maxHoldLevelKey: maybeEnvNumber("MIGRATION_MAX_HOLD_LEVEL_KEY"),
      enableStorageLayoutChecks: parseBool("MIGRATION_ENABLE_STORAGE_LAYOUT_CHECKS", true),
      enableReverseIndexChecks: parseBool("MIGRATION_ENABLE_REVERSE_INDEX_CHECKS", true),
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
    console.log("[migration test] RPC migration state diff: OK");
  });
});

