import { artifacts } from "hardhat";
import { ethers } from "ethers";
import { expect } from "chai";
import { loadEnv } from "../env";

/**
 * RPC checks for `soulsByAttribute[attribute]` (EnumerableSet populated in
 * SoulAttributeRegistry.setAttribute when value is non-zero).
 *
 * Discovers real soul IDs from SoulRegistry, keeps only souls with a non-zero
 * record for each attribute, then randomly samples those IDs so comparisons
 * cannot pass trivially on empty values.
 */

const EMPTY_BYTES20 = "0x0000000000000000000000000000000000000000";

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

function normalizeAddr(addr: string): string {
  return ethers.utils.getAddress(addr).toLowerCase();
}

function bytes20Lower(hex: string): string {
  return ethers.utils.hexlify(hex).toLowerCase();
}

/** Pick up to `sampleSize` distinct random elements from `items` (mutates copy). */
function randomSample<T>(items: T[], sampleSize: number): T[] {
  if (items.length === 0) return [];
  const pool = [...items];
  const n = Math.min(sampleSize, pool.length);
  const out: T[] = [];
  for (let k = 0; k < n; k++) {
    const j = Math.floor(Math.random() * pool.length);
    out.push(pool[j]);
    pool[j] = pool[pool.length - 1];
    pool.pop();
  }
  return out;
}

async function getRegisteredSoulIds(args: {
  soulRegistry: ethers.Contract;
  maxSoulIds?: number;
  blockTag?: number;
}): Promise<string[]> {
  const callOverrides = args.blockTag != null ? ({ blockTag: args.blockTag } as any) : ({} as any);
  const lastSoulId = (await args.soulRegistry.lastSoulId(callOverrides)).toNumber();
  const limit = args.maxSoulIds != null ? Math.min(args.maxSoulIds, lastSoulId) : lastSoulId;
  const ids: string[] = [];
  for (let i = 1; i <= limit; i++) {
    const primary = await args.soulRegistry.soulPrimaryAddress(i, callOverrides);
    if (primary !== ethers.constants.AddressZero) ids.push(String(i));
  }
  return ids;
}

async function findEnumerationIndex(args: {
  reg: ethers.Contract;
  attribute: string;
  soulId: string;
  count: number;
  overrides: any;
}): Promise<number> {
  for (let i = 0; i < args.count; i++) {
    const s = (await args.reg.soulByAttributeAtIndex(args.attribute, i, args.overrides)).toString();
    if (s === args.soulId) return i;
  }
  return -1;
}

describe("RPC SoulAttributeRegistry soulsByAttribute mapping", () => {
  const required = [
    "ORIGINAL_RPC_URL",
    "TARGET_RPC_URL",
    "SOUL_REGISTRY_ORIGINAL",
    "SOUL_REGISTRY_TARGET",
    "SOUL_ATTRIBUTE_REGISTRY_ORIGINAL",
    "SOUL_ATTRIBUTE_REGISTRY_TARGET",
    "HOLD_AMOUNT_ORIGINAL",
    "HOLD_AMOUNT_TARGET",
    "IS_VERIFIED_ORIGINAL",
    "IS_VERIFIED_TARGET",
  ];

  it("should match soulsByAttribute enumeration and records on random real soul IDs", async function () {
    loadEnv();
    if (!hasRequiredEnv(required)) {
      throw new Error(`Missing required ENV vars: ${required.join(", ")}`);
    }

    this.timeout(3000000);

    const sampleSize = maybeEnvNumber("MIGRATION_SOULS_BY_ATTR_SAMPLE_SIZE") ?? 5;
    const maxSoulIds = maybeEnvNumber("MIGRATION_MAX_SOUL_IDS");
    const originalBlockNumber = maybeEnvNumber("ORIGINAL_BLOCK_NUMBER");
    const targetBlockNumber = maybeEnvNumber("TARGET_BLOCK_NUMBER");

    const originalRpcUrl = mustEnv("ORIGINAL_RPC_URL");
    const targetRpcUrl = mustEnv("TARGET_RPC_URL");

    const soulRegOriginal = mustEnv("SOUL_REGISTRY_ORIGINAL");
    const soulRegTarget = mustEnv("SOUL_REGISTRY_TARGET");
    const regOriginal = mustEnv("SOUL_ATTRIBUTE_REGISTRY_ORIGINAL");
    const regTarget = mustEnv("SOUL_ATTRIBUTE_REGISTRY_TARGET");
    const holdOriginal = normalizeAddr(mustEnv("HOLD_AMOUNT_ORIGINAL"));
    const holdTarget = normalizeAddr(mustEnv("HOLD_AMOUNT_TARGET"));
    const verifiedOriginal = normalizeAddr(mustEnv("IS_VERIFIED_ORIGINAL"));
    const verifiedTarget = normalizeAddr(mustEnv("IS_VERIFIED_TARGET"));

    const sarArtifact = await artifacts.readArtifact("SoulAttributeRegistry");
    const srArtifact = await artifacts.readArtifact("SoulRegistry");

    const originalProvider = new ethers.providers.JsonRpcProvider(originalRpcUrl);
    const targetProvider = new ethers.providers.JsonRpcProvider(targetRpcUrl);
    const regO = new ethers.Contract(regOriginal, sarArtifact.abi, originalProvider);
    const regT = new ethers.Contract(regTarget, sarArtifact.abi, targetProvider);
    const soulO = new ethers.Contract(soulRegOriginal, srArtifact.abi, originalProvider);
    const soulT = new ethers.Contract(soulRegTarget, srArtifact.abi, targetProvider);

    const callOverridesOriginal =
      originalBlockNumber != null ? ({ blockTag: originalBlockNumber } as any) : ({} as any);
    const callOverridesTarget =
      targetBlockNumber != null ? ({ blockTag: targetBlockNumber } as any) : ({} as any);

    const lastO = (await soulO.lastSoulId(callOverridesOriginal)).toString();
    const lastT = (await soulT.lastSoulId(callOverridesTarget)).toString();
    expect(lastT, "SoulRegistry lastSoulId matches across chains").to.equal(lastO);

    const soulIds = await getRegisteredSoulIds({
      soulRegistry: soulO,
      maxSoulIds,
      blockTag: originalBlockNumber,
    });
    expect(soulIds.length, "at least one registered soul on original in scanned range").to.be.greaterThan(0);

    const attributes: Array<{
      label: string;
      original: string;
      target: string;
    }> = [
      { label: "HoldAmount", original: holdOriginal, target: holdTarget },
      { label: "IsVerified", original: verifiedOriginal, target: verifiedTarget },
    ];

    for (const attr of attributes) {
      const countO = (await regO.soulsCountByAttribute(attr.original, callOverridesOriginal)).toNumber();
      const countT = (await regT.soulsCountByAttribute(attr.target, callOverridesTarget)).toNumber();

      expect(countT, `${attr.label} soulsCountByAttribute target`).to.equal(countO);

      const withAttr: string[] = [];
      for (const soulId of soulIds) {
        const v = await regO.soulAttributeValue(soulId, attr.original, callOverridesOriginal);
        if (bytes20Lower(v) !== EMPTY_BYTES20) withAttr.push(soulId);
      }

      if (countO > 0 && withAttr.length === 0) {
        throw new Error(
          `${attr.label}: soulsCountByAttribute=${countO} but no non-zero records among ${soulIds.length} scanned souls. ` +
            `Raise MIGRATION_MAX_SOUL_IDS or scan the full registry range.`
        );
      }

      if (countO === 0) {
        expect(withAttr.length, `${attr.label} no enumeration implies no records in range`).to.equal(0);
        continue;
      }

      const sampled = randomSample(withAttr, sampleSize);
      expect(sampled.length, `${attr.label} sample from non-empty withAttr`).to.be.greaterThan(0);

      for (const soulId of sampled) {
        const valueO = await regO.soulAttributeValue(soulId, attr.original, callOverridesOriginal);
        const valueT = await regT.soulAttributeValue(soulId, attr.target, callOverridesTarget);
        expect(bytes20Lower(valueT), `${attr.label} soul ${soulId} value`).to.equal(bytes20Lower(valueO));
        expect(bytes20Lower(valueO), `${attr.label} soul ${soulId} must be non-zero`).to.not.equal(EMPTY_BYTES20);

        const setAtO = (await regO.soulAttributeSetAt(soulId, attr.original, callOverridesOriginal)).toString();
        const setAtT = (await regT.soulAttributeSetAt(soulId, attr.target, callOverridesTarget)).toString();
        const updatedO = (await regO.soulAttributeUpdatedAt(soulId, attr.original, callOverridesOriginal)).toString();
        const updatedT = (await regT.soulAttributeUpdatedAt(soulId, attr.target, callOverridesTarget)).toString();
        expect(setAtT, `${attr.label} soul ${soulId} setAt`).to.equal(setAtO);
        expect(updatedT, `${attr.label} soul ${soulId} updatedAt`).to.equal(updatedO);

        const idxO = await findEnumerationIndex({
          reg: regO,
          attribute: attr.original,
          soulId,
          count: countO,
          overrides: callOverridesOriginal,
        });
        const idxT = await findEnumerationIndex({
          reg: regT,
          attribute: attr.target,
          soulId,
          count: countT,
          overrides: callOverridesTarget,
        });
        expect(idxO, `${attr.label} soul ${soulId} in soulsByAttribute on original`).to.be.greaterThanOrEqual(0);
        expect(idxT, `${attr.label} soul ${soulId} in soulsByAttribute on target`).to.equal(idxO);

        const [enumSoulO, enumValO] = await regO.soulAndValueByAttributeAtIndex(
          attr.original,
          idxO,
          callOverridesOriginal
        );
        const [enumSoulT, enumValT] = await regT.soulAndValueByAttributeAtIndex(
          attr.target,
          idxT,
          callOverridesTarget
        );
        expect(enumSoulT.toString(), `${attr.label} enumeration soulId ${soulId}`).to.equal(enumSoulO.toString());
        expect(bytes20Lower(enumValT), `${attr.label} enumeration value ${soulId}`).to.equal(bytes20Lower(enumValO));
        expect(enumSoulO.toString(), `${attr.label} enumeration soul matches ${soulId}`).to.equal(soulId);
      }
    }
  });
});
