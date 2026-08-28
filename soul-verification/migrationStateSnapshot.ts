import fs from "fs";
import path from "path";
import { artifacts } from "hardhat";
import { ethers } from "ethers";
import type { Contract as EthersContract } from "ethers";

type Side = "original" | "target";

export type MigrationCheckConfig = {
  originalRpcUrl: string;
  targetRpcUrl: string;

  // Optional block pinning for deterministic reads.
  // If unset, reads come from the latest block.
  originalBlockNumber?: number;
  targetBlockNumber?: number;

  // Contract addresses on each chain.
  soulRegistryConfig: { original: string; target: string };
  soulRegistry: { original: string; target: string };
  soulAttributeRegistry: { original: string; target: string };
  soulBoundTokenRegistry: { original: string; target: string };
  holdAmount: { original: string; target: string };
  isVerified: { original: string; target: string };
  soulDrop: { original: string; target: string };

  // Optional canonicalization for token collections encoded into bytes32 tokens.
  // If omitted, unknown collections are compared by their raw address on each chain.
  knownTokenCollections?: Array<{
    label: string;
    originalAddress: string;
    targetAddress: string;
  }>;

  maxSoulIds?: number; // if set, checks only first N soul IDs starting from 1
  /** If set, only these soul IDs are snapshotted and storage-checked (both sides). Takes precedence over `maxSoulIds`. */
  sampleSoulIds?: string[];
  maxHoldLevelKey?: number; // default: 13
  enableStorageLayoutChecks?: boolean; // default: true
  enableReverseIndexChecks?: boolean; // default: false (full-chain reverse scans are expensive)
};

export type MigrationSnapshot = {
  soulRegistryConfig: { owner: string; maxAddressesPerSoul: string };
  holdAmount: { owner: string };
  isVerified: { owner: string };
  soulRegistry: {
    owner: string;
    lastSoulId: string;
    souls: Array<{
      id: string;
      primaryAddress: string;
      activeAddresses: string[];
    }>;
  };
  soulAttributeRegistry: {
    owner: string;
    featureStatuses: Record<string, { status: string; pausedAt: string }>;
    attributesBySoul: Array<{
      soulId: string;
      attributeLabels: string[];
      valuesByAttributeLabel: Record<
        string,
        { value: string; setAt: string; updatedAt: string }
      >;
    }>;
    soulsByAttributeLabel: Record<string, string[]>;
  };
  soulBoundTokenRegistry: {
    owner: string;
    tokensBySoul: Array<{
      soulId: string;
      tokens: Array<{ collectionLabel: string; tokenId: string }>;
    }>;
    soulsByTokenKey: Array<{
      tokenKey: { collectionLabel: string; tokenId: string };
      soulIds: string[];
    }>;
  };
  soulDrop: {
    owner: string;
    paused: boolean;
    contractBalance: string;
    percentByHoldLevel: Record<string, string>; // key -> uint256
    claimedAtBySoul: Record<string, string>; // soulId -> uint256
    withholdingsBySoul: Record<string, string>; // soulId -> uint256
  };
};

type BuildInfoStorageLayout = {
  storageLayout: any;
};

const BUILD_INFO_DIR = "artifacts/build-info";

function getLogEvery(): number {
  const v = process.env.MIGRATION_LOG_EVERY;
  if (!v) return 25;
  const n = Number(v);
  return Number.isFinite(n) && n > 0 ? Math.floor(n) : 25;
}

function logProgress(message: string): void {
  // Enabled by default; set MIGRATION_LOG_PROGRESS=0 to silence.
  if (process.env.MIGRATION_LOG_PROGRESS === "0") return;
  // eslint-disable-next-line no-console
  console.log(`[migration] ${message}`);
}

function normalizeAddr(addr: string): string {
  return ethers.utils.getAddress(addr).toLowerCase();
}

function slotToHex32(slot: bigint): string {
  const bn = ethers.BigNumber.from(slot.toString());
  return ethers.utils.hexZeroPad(ethers.utils.hexlify(bn), 32);
}

function decodeAddressFromBytes32(bytes32Hex: string): string {
  const hex = bytes32Hex.startsWith("0x") ? bytes32Hex.slice(2) : bytes32Hex;
  if (hex.length < 40) return ethers.constants.AddressZero;
  const addrHex = hex.slice(24); // last 20 bytes
  return normalizeAddr("0x" + addrHex);
}

function decodeUint256(raw32: string): string {
  return BigInt(raw32).toString();
}

function decodeTokenBytes32(tokenBytes32: string): { collectionAddress: string; tokenId: string } {
  const hex = tokenBytes32.startsWith("0x") ? tokenBytes32.slice(2) : tokenBytes32;
  const collectionHex = hex.slice(0, 40);
  const tokenIdHex = hex.slice(40); // 12 bytes = 24 hex chars
  return {
    collectionAddress: normalizeAddr("0x" + collectionHex),
    tokenId: BigInt("0x" + tokenIdHex).toString(),
  };
}

function decodePackedSoulAttributeRecord(raw32: string): {
  value: string; // bytes20
  setAt: string; // uint48
  updatedAt: string; // uint48
} {
  // Solidity packs these from the least-significant bytes:
  // struct Record { bytes20 value; uint48 setAt; uint48 updatedAt; }
  const word = BigInt(raw32);
  const valueMask = (1n << 160n) - 1n;
  const u48Mask = (1n << 48n) - 1n;

  const value = word & valueMask;
  const setAt = (word >> 160n) & u48Mask;
  const updatedAt = (word >> 208n) & u48Mask;

  return {
    value: ethers.utils.hexZeroPad("0x" + value.toString(16), 20).toLowerCase(),
    setAt: setAt.toString(),
    updatedAt: updatedAt.toString(),
  };
}

function findLatestBuildInfoPath(projectRoot: string): string {
  const dir = path.join(projectRoot, BUILD_INFO_DIR);
  const entries = fs
    .readdirSync(dir)
    .filter((f) => f.endsWith(".json"))
    .map((f) => ({
      full: path.join(dir, f),
      mtimeMs: fs.statSync(path.join(dir, f)).mtimeMs,
    }))
    .sort((a, b) => b.mtimeMs - a.mtimeMs);
  if (entries.length === 0) throw new Error(`No build-info JSON files found under ${dir}`);
  return entries[0].full;
}

function getBuildInfo(projectRoot: string): any {
  const buildInfoPath = findLatestBuildInfoPath(projectRoot);
  return JSON.parse(fs.readFileSync(buildInfoPath, "utf8"));
}

function getStorageLayoutForContract(buildInfo: any, contractBuildKey: { file: string; name: string }): BuildInfoStorageLayout {
  const entry = buildInfo.output?.contracts?.[contractBuildKey.file]?.[contractBuildKey.name];
  if (!entry?.storageLayout) {
    throw new Error(`Missing storageLayout for ${contractBuildKey.file}:${contractBuildKey.name}`);
  }
  return { storageLayout: entry.storageLayout };
}

function getVarBaseSlot(storageLayout: any, varLabel: string): bigint {
  const found = storageLayout.storage.find((x: any) => x.label === varLabel);
  if (!found) throw new Error(`Missing storage var slot: ${varLabel}`);
  return BigInt(found.slot);
}

function mappingEntrySlot(args: { baseSlot: bigint; keyType: string; keyValue: any }): bigint {
  // mapping slot = keccak256(abi.encode(key, baseSlot))
  const baseSlotEnc = args.baseSlot.toString();
  const keyValueEnc = typeof args.keyValue === "bigint" ? args.keyValue.toString() : args.keyValue;
  const encoded = ethers.utils.defaultAbiCoder.encode([args.keyType, "uint256"], [keyValueEnc, baseSlotEnc]);
  return BigInt(ethers.utils.keccak256(encoded));
}

function nestedMappingEntrySlot(args: {
  outerBaseSlot: bigint;
  outerKeyType: string;
  outerKeyValue: any;
  innerKeyType: string;
  innerKeyValue: any;
}): bigint {
  const outerSlot = mappingEntrySlot({
    baseSlot: args.outerBaseSlot,
    keyType: args.outerKeyType,
    keyValue: args.outerKeyValue,
  });
  return mappingEntrySlot({
    baseSlot: outerSlot,
    keyType: args.innerKeyType,
    keyValue: args.innerKeyValue,
  });
}

function dynamicArrayDataBase(slot: bigint): bigint {
  // bytes32 keccak256(abi.encodePacked(slot))
  const slotWord = slotToHex32(slot);
  return BigInt(ethers.utils.keccak256(slotWord));
}

function collectionLabelForAddress(config: MigrationCheckConfig, side: Side, addr: string): string {
  const a = normalizeAddr(addr);
  for (const k of config.knownTokenCollections ?? []) {
    if (side === "original" && a === normalizeAddr(k.originalAddress)) return k.label;
    if (side === "target" && a === normalizeAddr(k.targetAddress)) return k.label;
    // if both are the same address on both chains, allow matching either side
    if (a === normalizeAddr(k.originalAddress) && a === normalizeAddr(k.targetAddress)) return k.label;
  }
  return a;
}

function attributeLabelForAddress(side: Side, config: MigrationCheckConfig, addr: string): string {
  const a = normalizeAddr(addr);
  if (a === normalizeAddr(config.holdAmount[side])) return "HoldAmount";
  if (a === normalizeAddr(config.isVerified[side])) return "IsVerified";
  return a;
}

function soulIdListEqual(a: MigrationSnapshot["soulRegistry"]["souls"], b: MigrationSnapshot["soulRegistry"]["souls"]): boolean {
  if (a.length !== b.length) return false;
  for (let i = 0; i < a.length; i++) {
    if (a[i].id !== b[i].id) return false;
  }
  return true;
}

/** Deduplicates, validates uint256 soul IDs, and sorts ascending. */
export function normalizeSampleSoulIds(ids: string[]): string[] {
  const seen = new Set<string>();
  const out: string[] = [];
  for (const raw of ids) {
    const s = String(raw).trim();
    if (!s) continue;
    const id = BigInt(s).toString();
    if (seen.has(id)) continue;
    seen.add(id);
    out.push(id);
  }
  out.sort((a, b) => (BigInt(a) < BigInt(b) ? -1 : BigInt(a) > BigInt(b) ? 1 : 0));
  return out;
}

async function getSoulIdsFromSoulRegistry(args: {
  soulRegistry: EthersContract;
  maxSoulIds?: number;
  logPrefix?: string;
  blockTag?: number;
}): Promise<string[]> {
  const callOverrides = args.blockTag != null ? ({ blockTag: args.blockTag } as any) : ({} as any);
  const lastSoulId = (await (args.soulRegistry as any).lastSoulId(callOverrides)).toNumber();
  const limit = args.maxSoulIds != null ? Math.min(args.maxSoulIds, lastSoulId) : lastSoulId;
  const ids: string[] = [];
  const logEvery = getLogEvery();
  for (let i = 1; i <= limit; i++) {
    const primary = await (args.soulRegistry as any).soulPrimaryAddress(i, callOverrides);
    if (primary !== ethers.constants.AddressZero) ids.push(String(i));

    if (i === 1 || i === limit || i % logEvery === 0) {
      const prefix = args.logPrefix ? `${args.logPrefix} ` : "";
      logProgress(`${prefix}discover souls checked=${i}/${limit} found=${ids.length}`);
    }
  }
  return ids;
}

export async function snapshotMigrationState(config: MigrationCheckConfig, side: Side): Promise<MigrationSnapshot> {
  logProgress(`snapshot start side=${side}`);
  const rpcUrl = side === "original" ? config.originalRpcUrl : config.targetRpcUrl;
  const provider = new ethers.providers.JsonRpcProvider(rpcUrl);
  const blockTag = side === "original" ? config.originalBlockNumber : config.targetBlockNumber;
  const callOverrides = blockTag != null ? ({ blockTag } as any) : ({} as any);

  const soulRegistryConfigAddress = side === "original" ? config.soulRegistryConfig.original : config.soulRegistryConfig.target;
  const soulRegistryAddress = side === "original" ? config.soulRegistry.original : config.soulRegistry.target;
  const soulAttributeRegistryAddress = side === "original" ? config.soulAttributeRegistry.original : config.soulAttributeRegistry.target;
  const soulBoundTokenRegistryAddress = side === "original" ? config.soulBoundTokenRegistry.original : config.soulBoundTokenRegistry.target;
  const holdAmountAddress = side === "original" ? config.holdAmount.original : config.holdAmount.target;
  const isVerifiedAddress = side === "original" ? config.isVerified.original : config.isVerified.target;
  const soulDropAddress = side === "original" ? config.soulDrop.original : config.soulDrop.target;

  const SoulRegistryConfigArtifact = await artifacts.readArtifact("SoulRegistryConfig");
  const SoulRegistryArtifact = await artifacts.readArtifact("SoulRegistry");
  const SoulAttributeRegistryArtifact = await artifacts.readArtifact("SoulAttributeRegistry");
  const SoulBoundTokenRegistryArtifact = await artifacts.readArtifact("SoulBoundTokenRegistry");
  const SoulDropArtifact = await artifacts.readArtifact("SoulDrop");
  const HoldAmountArtifact = await artifacts.readArtifact("HoldAmount");
  const IsVerifiedArtifact = await artifacts.readArtifact("IsVerified");

  const soulRegistryConfig = new ethers.Contract(soulRegistryConfigAddress, SoulRegistryConfigArtifact.abi, provider);
  const soulRegistry = new ethers.Contract(soulRegistryAddress, SoulRegistryArtifact.abi, provider);
  const soulAttributeRegistry = new ethers.Contract(soulAttributeRegistryAddress, SoulAttributeRegistryArtifact.abi, provider);
  const soulBoundTokenRegistry = new ethers.Contract(soulBoundTokenRegistryAddress, SoulBoundTokenRegistryArtifact.abi, provider);
  const soulDrop = new ethers.Contract(soulDropAddress, SoulDropArtifact.abi, provider);
  const holdAmountContract = new ethers.Contract(holdAmountAddress, HoldAmountArtifact.abi, provider);
  const isVerifiedContract = new ethers.Contract(isVerifiedAddress, IsVerifiedArtifact.abi, provider);

  let souls: string[];
  if (config.sampleSoulIds?.length) {
    souls = normalizeSampleSoulIds(config.sampleSoulIds);
    if (souls.length === 0) {
      throw new Error("sampleSoulIds resolved to an empty list after normalization");
    }
    logProgress(`snapshot: using sampleSoulIds count=${souls.length} side=${side}`);
  } else {
    souls = await getSoulIdsFromSoulRegistry({
      soulRegistry,
      maxSoulIds: config.maxSoulIds,
      logPrefix: `snapshot(${side})`,
      blockTag,
    });
    logProgress(`snapshot: discovered soulIds=${souls.length} side=${side}`);
  }
  const soulObjs: MigrationSnapshot["soulRegistry"]["souls"] = [];
  for (let idx = 0; idx < souls.length; idx++) {
    const idStr = souls[idx];
    const id = Number(idStr);
    const primaryAddress = normalizeAddr(await soulRegistry.soulPrimaryAddress(id, callOverrides));
    const activeAddressesRaw: string[] = await soulRegistry.soulAddresses(id, callOverrides);
    const activeAddresses = activeAddressesRaw.map((a) => normalizeAddr(a));
    soulObjs.push({ id: idStr, primaryAddress, activeAddresses });
    const logEvery = getLogEvery();
    if (idx === 0 || idx === souls.length - 1 || (idx + 1) % logEvery === 0) {
      logProgress(`snapshot(${side}) soulRegistry ${idx + 1}/${souls.length}`);
    }
  }

  const snapshot: MigrationSnapshot = {
    soulRegistryConfig: {
      owner: normalizeAddr(await soulRegistryConfig.owner(callOverrides)),
      maxAddressesPerSoul: (await soulRegistryConfig.maxAddressesPerSoul(callOverrides)).toString(),
    },
    holdAmount: { owner: normalizeAddr(await holdAmountContract.owner(callOverrides)) },
    isVerified: { owner: normalizeAddr(await isVerifiedContract.owner(callOverrides)) },
    soulRegistry: {
      owner: normalizeAddr(await soulRegistry.owner(callOverrides)),
      lastSoulId: (await soulRegistry.lastSoulId(callOverrides)).toString(),
      souls: soulObjs,
    },
    soulAttributeRegistry: {
      owner: normalizeAddr(await soulAttributeRegistry.owner(callOverrides)),
      featureStatuses: {
        HoldAmount: {
          status: (await soulAttributeRegistry.featureStatus(holdAmountAddress, callOverrides)).toString(),
          pausedAt: (await soulAttributeRegistry.featurePausedAt(holdAmountAddress, callOverrides)).toString(),
        },
        IsVerified: {
          status: (await soulAttributeRegistry.featureStatus(isVerifiedAddress, callOverrides)).toString(),
          pausedAt: (await soulAttributeRegistry.featurePausedAt(isVerifiedAddress, callOverrides)).toString(),
        },
      },
      attributesBySoul: [],
      soulsByAttributeLabel: {
        HoldAmount: [],
        IsVerified: [],
      },
    },
    soulBoundTokenRegistry: {
      owner: normalizeAddr(await soulBoundTokenRegistry.owner(callOverrides)),
      tokensBySoul: [],
      soulsByTokenKey: [],
    },
    soulDrop: {
      owner: normalizeAddr(await soulDrop.owner(callOverrides)),
      paused: await soulDrop.paused(callOverrides),
      contractBalance: (await provider.getBalance(soulDrop.address, blockTag)).toString(),
      percentByHoldLevel: {},
      claimedAtBySoul: {},
      withholdingsBySoul: {},
    },
  };

  // SoulAttributeRegistry: attributesBySoul + records values for known attributes.
  for (let idx = 0; idx < souls.length; idx++) {
    const soulIdStr = souls[idx];
    const soulId = Number(soulIdStr);

    const attributeCount = (await soulAttributeRegistry.attributesCountBySoul(soulId, callOverrides)).toNumber();
    const attributeLabels: string[] = [];
    for (let i = 0; i < attributeCount; i++) {
      const attrAddr = await soulAttributeRegistry.attributeBySoulAtIndex(soulId, i, callOverrides);
      attributeLabels.push(attributeLabelForAddress(side, config, attrAddr));
    }

    const holdValue = await soulAttributeRegistry.soulAttributeValue(soulId, holdAmountAddress, callOverrides);
    const holdSetAt = (await soulAttributeRegistry.soulAttributeSetAt(soulId, holdAmountAddress, callOverrides)).toString();
    const holdUpdatedAt = (await soulAttributeRegistry.soulAttributeUpdatedAt(soulId, holdAmountAddress, callOverrides)).toString();

    const verifiedValue = await soulAttributeRegistry.soulAttributeValue(soulId, isVerifiedAddress, callOverrides);
    const verifiedSetAt = (await soulAttributeRegistry.soulAttributeSetAt(soulId, isVerifiedAddress, callOverrides)).toString();
    const verifiedUpdatedAt = (await soulAttributeRegistry.soulAttributeUpdatedAt(soulId, isVerifiedAddress, callOverrides)).toString();

    snapshot.soulAttributeRegistry.attributesBySoul.push({
      soulId: soulIdStr,
      attributeLabels,
      valuesByAttributeLabel: {
        HoldAmount: { value: holdValue.toLowerCase(), setAt: holdSetAt, updatedAt: holdUpdatedAt },
        IsVerified: { value: verifiedValue.toLowerCase(), setAt: verifiedSetAt, updatedAt: verifiedUpdatedAt },
      },
    });
    const logEvery = getLogEvery();
    if (idx === 0 || idx === souls.length - 1 || (idx + 1) % logEvery === 0) {
      logProgress(`snapshot(${side}) soulAttributeRegistry ${idx + 1}/${souls.length}`);
    }
  }

  if (config.enableReverseIndexChecks) {
    for (const [label, addr] of [
      ["HoldAmount", holdAmountAddress],
      ["IsVerified", isVerifiedAddress],
    ] as Array<[string, string]>) {
      const count = (await soulAttributeRegistry.soulsCountByAttribute(addr, callOverrides)).toNumber();
      const soulIdsByAttr: string[] = [];
      for (let i = 0; i < count; i++) {
        soulIdsByAttr.push((await soulAttributeRegistry.soulByAttributeAtIndex(addr, i, callOverrides)).toString());
      }
      snapshot.soulAttributeRegistry.soulsByAttributeLabel[label] = soulIdsByAttr;
    }
  } else {
    // Fast path: build reverse lists from selected souls only.
    snapshot.soulAttributeRegistry.soulsByAttributeLabel.HoldAmount = [];
    snapshot.soulAttributeRegistry.soulsByAttributeLabel.IsVerified = [];
    for (const row of snapshot.soulAttributeRegistry.attributesBySoul) {
      if (row.valuesByAttributeLabel.HoldAmount?.value !== "0x0000000000000000000000000000000000000000") {
        snapshot.soulAttributeRegistry.soulsByAttributeLabel.HoldAmount.push(row.soulId);
      }
      if (row.valuesByAttributeLabel.IsVerified?.value !== "0x0000000000000000000000000000000000000000") {
        snapshot.soulAttributeRegistry.soulsByAttributeLabel.IsVerified.push(row.soulId);
      }
    }
    logProgress(`snapshot(${side}) reverse-index fast path for SoulAttributeRegistry`);
  }

  // SoulBoundTokenRegistry: tokensBySoul + soulsByTokenKey.
  const uniqueTokenKeys = new Map<string, { collectionLabel: string; tokenId: string }>();
  const uniqueTokenBytesByKey = new Map<string, string>(); // for enumerating soulsByToken, keyed by canonical token key

  for (let idx = 0; idx < souls.length; idx++) {
    const soulIdStr = souls[idx];
    const soulId = Number(soulIdStr);
    const count = (await soulBoundTokenRegistry.tokensCountBySoul(soulId, callOverrides)).toNumber();
    const tokens: Array<{ collectionLabel: string; tokenId: string }> = [];
    for (let i = 0; i < count; i++) {
      const tokenBytes32: string = await soulBoundTokenRegistry.tokenBySoulAtIndex(soulId, i, callOverrides);
      const decoded = decodeTokenBytes32(tokenBytes32);
      const collectionLabel = collectionLabelForAddress(config, side, decoded.collectionAddress);
      const tokenKeyStr = `${collectionLabel}:${decoded.tokenId}`;
      uniqueTokenKeys.set(tokenKeyStr, { collectionLabel, tokenId: decoded.tokenId });
      uniqueTokenBytesByKey.set(tokenKeyStr, tokenBytes32);
      tokens.push({ collectionLabel, tokenId: decoded.tokenId });
    }
    snapshot.soulBoundTokenRegistry.tokensBySoul.push({ soulId: soulIdStr, tokens });

    const logEvery = getLogEvery();
    if (idx === 0 || idx === souls.length - 1 || (idx + 1) % logEvery === 0) {
      logProgress(`snapshot(${side}) soulBoundTokenRegistry tokensBySoul ${idx + 1}/${souls.length}`);
    }
  }

  const sortedTokenKeys = Array.from(uniqueTokenKeys.entries())
    .map(([k, v]) => ({ k, v }))
    .sort((a, b) => {
      const c = a.v.collectionLabel.localeCompare(b.v.collectionLabel);
      if (c !== 0) return c;
      return BigInt(a.v.tokenId).toString().localeCompare(BigInt(b.v.tokenId).toString());
    });

  if (config.enableReverseIndexChecks) {
    for (const { k, v } of sortedTokenKeys) {
      const tokenBytes32 = uniqueTokenBytesByKey.get(k);
      if (!tokenBytes32) continue;
      const count = (await soulBoundTokenRegistry.soulsCountByToken(tokenBytes32, callOverrides)).toNumber();
      const soulIdsForToken: string[] = [];
      for (let i = 0; i < count; i++) {
        soulIdsForToken.push((await soulBoundTokenRegistry.soulByTokenAtIndex(tokenBytes32, i, callOverrides)).toString());
      }
      snapshot.soulBoundTokenRegistry.soulsByTokenKey.push({
        tokenKey: { collectionLabel: v.collectionLabel, tokenId: v.tokenId },
        soulIds: soulIdsForToken,
      });
    }
  } else {
    // Fast path: derive reverse index from selected souls only.
    const reverseMap = new Map<string, { tokenKey: { collectionLabel: string; tokenId: string }; soulIds: string[] }>();
    for (const row of snapshot.soulBoundTokenRegistry.tokensBySoul) {
      for (const tok of row.tokens) {
        const key = `${tok.collectionLabel}:${tok.tokenId}`;
        const found = reverseMap.get(key);
        if (found) {
          found.soulIds.push(row.soulId);
        } else {
          reverseMap.set(key, {
            tokenKey: { collectionLabel: tok.collectionLabel, tokenId: tok.tokenId },
            soulIds: [row.soulId],
          });
        }
      }
    }
    snapshot.soulBoundTokenRegistry.soulsByTokenKey = Array.from(reverseMap.values()).sort((a, b) => {
      const c = a.tokenKey.collectionLabel.localeCompare(b.tokenKey.collectionLabel);
      if (c !== 0) return c;
      return BigInt(a.tokenKey.tokenId).toString().localeCompare(BigInt(b.tokenKey.tokenId).toString());
    });
    logProgress(`snapshot(${side}) reverse-index fast path for SoulBoundTokenRegistry`);
  }

  logProgress(`snapshot(${side}) SBT soulsByTokenKey tokenKeys=${snapshot.soulBoundTokenRegistry.soulsByTokenKey.length}`);

  // SoulDrop: paused state, percent mapping + per-soul claimed/withholdings
  const maxHoldLevelKey = config.maxHoldLevelKey ?? 13;
  for (let k = 0; k <= maxHoldLevelKey; k++) {
    snapshot.soulDrop.percentByHoldLevel[String(k)] = (await soulDrop.percentByHoldLevel(k, callOverrides)).toString();
  }
  for (let idx = 0; idx < souls.length; idx++) {
    const soulIdStr = souls[idx];
    const soulId = Number(soulIdStr);
    snapshot.soulDrop.claimedAtBySoul[soulIdStr] = (await soulDrop.claimedAtBySoul(soulId, callOverrides)).toString();
    snapshot.soulDrop.withholdingsBySoul[soulIdStr] = (await soulDrop.withholdingsBySoul(soulId, callOverrides)).toString();

    const logEvery = getLogEvery();
    if (idx === 0 || idx === souls.length - 1 || (idx + 1) % logEvery === 0) {
      logProgress(`snapshot(${side}) soulDrop perSoul ${idx + 1}/${souls.length}`);
    }
  }

  logProgress(`snapshot done side=${side}`);
  return snapshot;
}

function assertSnapshotsEqual(original: MigrationSnapshot, target: MigrationSnapshot): void {
  // Deep equality gives a strong guarantee for externally observable state.
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  const o = original as any;
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  const t = target as any;
  if (JSON.stringify(o) !== JSON.stringify(t)) {
    throw new Error(`State mismatch between original and target (compare JSON snapshots).`);
  }
}

function reconstructTokenBytes32ForSide(args: {
  config: MigrationCheckConfig;
  side: Side;
  tokenKey: { collectionLabel: string; tokenId: string };
}): string | null {
  const { config, side, tokenKey } = args;
  const found = (config.knownTokenCollections ?? []).find((x) => x.label === tokenKey.collectionLabel);
  if (!found) return null;
  const collectionAddr = side === "original" ? found.originalAddress : found.targetAddress;
  const addrHex = normalizeAddr(collectionAddr).slice(2); // 40 chars
  const tokenIdBN = ethers.BigNumber.from(tokenKey.tokenId);
  let tokenIdHex = tokenIdBN.toHexString().slice(2); // no 0x
  tokenIdHex = tokenIdHex.padStart(24, "0"); // uint96 = 12 bytes = 24 hex
  return "0x" + addrHex + tokenIdHex;
}

export async function assertMigrationStateMatches(config: MigrationCheckConfig): Promise<void> {
  const enableStorageLayoutChecks = config.enableStorageLayoutChecks ?? true;

  if (config.sampleSoulIds?.length) {
    const ids = normalizeSampleSoulIds(config.sampleSoulIds);
    logProgress(`assertMigrationStateMatches: sample spot-check soulIds=${ids.join(",")} (subset; not a full proof)`);
  }
  logProgress("assertMigrationStateMatches start (snapshotting both sides)");

  const [originalSnapshot, targetSnapshot] = await Promise.all([
    snapshotMigrationState(config, "original"),
    snapshotMigrationState(config, "target"),
  ]);

  logProgress("assertMigrationStateMatches: snapshots captured; verifying equality");
  assertSnapshotsEqual(originalSnapshot, targetSnapshot);

  if (!enableStorageLayoutChecks) return;

  const projectRoot = process.cwd();
  const buildInfo = getBuildInfo(projectRoot);

  const originalProvider = new ethers.providers.JsonRpcProvider(config.originalRpcUrl);
  const targetProvider = new ethers.providers.JsonRpcProvider(config.targetRpcUrl);

  const maxHoldLevelKey = config.maxHoldLevelKey ?? 13;

  const souls = originalSnapshot.soulRegistry.souls;
  const originalBlockTag = config.originalBlockNumber;
  const targetBlockTag = config.targetBlockNumber;
  const logEvery = getLogEvery();

  // --- SoulRegistry storage checks
  const srLayout = getStorageLayoutForContract(buildInfo, { file: "contracts/SoulRegistry.sol", name: "SoulRegistry" }).storageLayout;
  const soulsByAddressBase = getVarBaseSlot(srLayout, "soulsByAddress");
  const primaryAddressesBySoulBase = getVarBaseSlot(srLayout, "primaryAddressesBySoul");
  const addressListsBySoulBase = getVarBaseSlot(srLayout, "addressListsBySoul");

  logProgress(`assert: SoulRegistry storage checks souls=${souls.length}`);
  for (let soulIdx = 0; soulIdx < souls.length; soulIdx++) {
    const soul = souls[soulIdx];
    const soulId = BigInt(soul.id);

    // primaryAddressesBySoul: mapping(uint256 => address)
    const primarySlot = mappingEntrySlot({ baseSlot: primaryAddressesBySoulBase, keyType: "uint256", keyValue: soulId });
    const rawPrimaryO = await originalProvider.getStorageAt(config.soulRegistry.original, slotToHex32(primarySlot), originalBlockTag);
    const rawPrimaryT = await targetProvider.getStorageAt(config.soulRegistry.target, slotToHex32(primarySlot), targetBlockTag);
    const primaryO = decodeAddressFromBytes32(rawPrimaryO);
    const primaryT = decodeAddressFromBytes32(rawPrimaryT);
    if (primaryO !== primaryT || primaryO !== normalizeAddr(soul.primaryAddress)) {
      throw new Error(`SoulRegistry primary mismatch for soulId=${soul.id}`);
    }

    // addressListsBySoul: mapping(uint256 => EnumerableSet.AddressSet)
    const setEntrySlot = mappingEntrySlot({ baseSlot: addressListsBySoulBase, keyType: "uint256", keyValue: soulId });
    const rawLenO = await originalProvider.getStorageAt(config.soulRegistry.original, slotToHex32(setEntrySlot), originalBlockTag);
    const rawLenT = await targetProvider.getStorageAt(config.soulRegistry.target, slotToHex32(setEntrySlot), targetBlockTag);
    const lenO = BigInt(rawLenO).toString();
    const lenT = BigInt(rawLenT).toString();
    if (lenO !== lenT || lenO !== String(soul.activeAddresses.length)) {
      throw new Error(`SoulRegistry active set length mismatch for soulId=${soul.id}`);
    }

    const valuesBase = dynamicArrayDataBase(setEntrySlot);
    for (let i = 0; i < soul.activeAddresses.length; i++) {
      const elemSlot = valuesBase + BigInt(i);
      const rawElemO = await originalProvider.getStorageAt(config.soulRegistry.original, slotToHex32(elemSlot), originalBlockTag);
      const rawElemT = await targetProvider.getStorageAt(config.soulRegistry.target, slotToHex32(elemSlot), targetBlockTag);
      const addrO = decodeAddressFromBytes32(rawElemO);
      const addrT = decodeAddressFromBytes32(rawElemT);
      const expected = normalizeAddr(soul.activeAddresses[i]);
      if (addrO !== addrT || addrO !== expected) {
        throw new Error(`SoulRegistry active address mismatch for soulId=${soul.id}, index=${i}`);
      }
    }

    // soulsByAddress mapping for each active address.
    for (const addr of soul.activeAddresses) {
      const addrNorm = normalizeAddr(addr);
      const slot = mappingEntrySlot({ baseSlot: soulsByAddressBase, keyType: "address", keyValue: addrNorm });
      const rawSoulIdO = await originalProvider.getStorageAt(config.soulRegistry.original, slotToHex32(slot), originalBlockTag);
      const rawSoulIdT = await targetProvider.getStorageAt(config.soulRegistry.target, slotToHex32(slot), targetBlockTag);
      const soulIdO = BigInt(rawSoulIdO).toString();
      const soulIdT = BigInt(rawSoulIdT).toString();
      if (soulIdO !== soulIdT || soulIdO !== soul.id) {
        throw new Error(`SoulRegistry soulsByAddress mismatch for addr=${addrNorm}`);
      }
    }

    if (soulIdx === 0 || soulIdx === souls.length - 1 || (soulIdx + 1) % logEvery === 0) {
      logProgress(`assert: SoulRegistry storage ${soulIdx + 1}/${souls.length}`);
    }
  }

  // --- SoulAttributeRegistry storage checks
  const arLayout = getStorageLayoutForContract(buildInfo, { file: "contracts/SoulAttributeRegistry.sol", name: "SoulAttributeRegistry" }).storageLayout;
  const attributesBySoulBase = getVarBaseSlot(arLayout, "attributesBySoul");
  const recordsBase = getVarBaseSlot(arLayout, "records");

  const holdAmountO = normalizeAddr(config.holdAmount.original);
  const isVerifiedO = normalizeAddr(config.isVerified.original);
  const holdAmountT = normalizeAddr(config.holdAmount.target);
  const isVerifiedT = normalizeAddr(config.isVerified.target);

  logProgress(`assert: SoulAttributeRegistry storage checks souls=${souls.length}`);
  for (let soulIdx = 0; soulIdx < souls.length; soulIdx++) {
    const soul = souls[soulIdx];
    const soulId = BigInt(soul.id);
    const attrBySoul = originalSnapshot.soulAttributeRegistry.attributesBySoul.find((x) => x.soulId === soul.id);
    const attrBySoulT = targetSnapshot.soulAttributeRegistry.attributesBySoul.find((x) => x.soulId === soul.id);
    if (!attrBySoul || !attrBySoulT) throw new Error(`Missing attribute snapshot for soulId=${soul.id}`);

    // attributesBySoul set
    const setEntrySlot = mappingEntrySlot({ baseSlot: attributesBySoulBase, keyType: "uint256", keyValue: soulId });
    const rawLenO = await originalProvider.getStorageAt(config.soulAttributeRegistry.original, slotToHex32(setEntrySlot), originalBlockTag);
    const rawLenT = await targetProvider.getStorageAt(config.soulAttributeRegistry.target, slotToHex32(setEntrySlot), targetBlockTag);
    const lenO = BigInt(rawLenO).toString();
    const lenT = BigInt(rawLenT).toString();
    if (lenO !== lenT || lenO !== String(attrBySoul.attributeLabels.length)) {
      throw new Error(`SoulAttributeRegistry attributesBySoul length mismatch for soulId=${soul.id}`);
    }

    const valuesBase = dynamicArrayDataBase(setEntrySlot);
    for (let i = 0; i < attrBySoul.attributeLabels.length; i++) {
      const elemSlot = valuesBase + BigInt(i);
      const rawElemO = await originalProvider.getStorageAt(config.soulAttributeRegistry.original, slotToHex32(elemSlot), originalBlockTag);
      const rawElemT = await targetProvider.getStorageAt(config.soulAttributeRegistry.target, slotToHex32(elemSlot), targetBlockTag);
      const attrAddrO = decodeAddressFromBytes32(rawElemO);
      const attrAddrT = decodeAddressFromBytes32(rawElemT);
      const labelO = attributeLabelForAddress("original", config, attrAddrO);
      const labelT = attributeLabelForAddress("target", config, attrAddrT);
      const expectedLabel = attrBySoul.attributeLabels[i];
      if (labelO !== labelT || labelO !== expectedLabel) {
        throw new Error(`SoulAttributeRegistry attributesBySoul mismatch for soulId=${soul.id}, index=${i}`);
      }
    }

    // records mapping for known attributes
    const attributesToCheck: Array<{ label: "HoldAmount" | "IsVerified"; addrO: string; addrT: string }> = [
      { label: "HoldAmount", addrO: holdAmountO, addrT: holdAmountT },
      { label: "IsVerified", addrO: isVerifiedO, addrT: isVerifiedT },
    ];

    for (const a of attributesToCheck) {
      const expected = attrBySoul.valuesByAttributeLabel[a.label];
      if (!expected) throw new Error(`Missing expected record snapshot for ${a.label}, soulId=${soul.id}`);

      const recordSlotO = nestedMappingEntrySlot({
        outerBaseSlot: recordsBase,
        outerKeyType: "uint256",
        outerKeyValue: soulId,
        innerKeyType: "address",
        innerKeyValue: a.addrO,
      });
      const recordSlotT = nestedMappingEntrySlot({
        outerBaseSlot: recordsBase,
        outerKeyType: "uint256",
        outerKeyValue: soulId,
        innerKeyType: "address",
        innerKeyValue: a.addrT,
      });

      const rawRecordO = await originalProvider.getStorageAt(config.soulAttributeRegistry.original, slotToHex32(recordSlotO), originalBlockTag);
      const rawRecordT = await targetProvider.getStorageAt(config.soulAttributeRegistry.target, slotToHex32(recordSlotT), targetBlockTag);
      const decodedO = decodePackedSoulAttributeRecord(rawRecordO);
      const decodedT = decodePackedSoulAttributeRecord(rawRecordT);

      if (decodedO.value.toLowerCase() !== decodedT.value.toLowerCase()) {
        throw new Error(
          `SoulAttributeRegistry records.value mismatch for soulId=${soul.id}, attr=${a.label}; ` +
          `original=${decodedO.value.toLowerCase()} target=${decodedT.value.toLowerCase()} expected=${expected.value.toLowerCase()}`
        );
      }
      if (decodedO.setAt !== decodedT.setAt || decodedO.updatedAt !== decodedT.updatedAt) {
        throw new Error(
          `SoulAttributeRegistry records.timestamps mismatch for soulId=${soul.id}, attr=${a.label}; ` +
          `original={setAt:${decodedO.setAt},updatedAt:${decodedO.updatedAt}} ` +
          `target={setAt:${decodedT.setAt},updatedAt:${decodedT.updatedAt}} ` +
          `expected={setAt:${expected.setAt},updatedAt:${expected.updatedAt}}`
        );
      }
      if (decodedO.value.toLowerCase() !== expected.value.toLowerCase() || decodedO.setAt !== expected.setAt || decodedO.updatedAt !== expected.updatedAt) {
        throw new Error(
          `SoulAttributeRegistry records mismatch vs snapshot for soulId=${soul.id}, attr=${a.label}; ` +
          `original={value:${decodedO.value.toLowerCase()},setAt:${decodedO.setAt},updatedAt:${decodedO.updatedAt}} ` +
          `target={value:${decodedT.value.toLowerCase()},setAt:${decodedT.setAt},updatedAt:${decodedT.updatedAt}} ` +
          `expected={value:${expected.value.toLowerCase()},setAt:${expected.setAt},updatedAt:${expected.updatedAt}}`
        );
      }
    }

    if (soulIdx === 0 || soulIdx === souls.length - 1 || (soulIdx + 1) % logEvery === 0) {
      logProgress(`assert: SoulAttributeRegistry storage ${soulIdx + 1}/${souls.length}`);
    }
  }

  // --- SoulBoundTokenRegistry storage checks
  const trLayout = getStorageLayoutForContract(buildInfo, { file: "contracts/SoulBoundTokenRegistry.sol", name: "SoulBoundTokenRegistry" }).storageLayout;
  const tokensBySoulBase = getVarBaseSlot(trLayout, "tokensBySoul");
  const soulsByTokenBase = getVarBaseSlot(trLayout, "soulsByToken");

  logProgress(`assert: SoulBoundTokenRegistry storage checks souls=${souls.length}`);
  for (let soulIdx = 0; soulIdx < souls.length; soulIdx++) {
    const soul = souls[soulIdx];
    const soulId = BigInt(soul.id);
    const expectedTokensBySoul = originalSnapshot.soulBoundTokenRegistry.tokensBySoul.find((x) => x.soulId === soul.id);
    const expectedTokensBySoulT = targetSnapshot.soulBoundTokenRegistry.tokensBySoul.find((x) => x.soulId === soul.id);
    if (!expectedTokensBySoul || !expectedTokensBySoulT) throw new Error(`Missing token snapshot for soulId=${soul.id}`);
    if (expectedTokensBySoul.tokens.length !== expectedTokensBySoulT.tokens.length) {
      throw new Error(`SoulBoundTokenRegistry token length mismatch for soulId=${soul.id}`);
    }

    const setEntrySlot = mappingEntrySlot({ baseSlot: tokensBySoulBase, keyType: "uint256", keyValue: soulId });
    const rawLenO = await originalProvider.getStorageAt(config.soulBoundTokenRegistry.original, slotToHex32(setEntrySlot), originalBlockTag);
    const rawLenT = await targetProvider.getStorageAt(config.soulBoundTokenRegistry.target, slotToHex32(setEntrySlot), targetBlockTag);
    const lenO = BigInt(rawLenO).toString();
    const lenT = BigInt(rawLenT).toString();
    if (lenO !== lenT || lenO !== String(expectedTokensBySoul.tokens.length)) {
      throw new Error(`SoulBoundTokenRegistry tokensBySoul length mismatch for soulId=${soul.id}`);
    }

    const valuesBase = dynamicArrayDataBase(setEntrySlot);
    for (let i = 0; i < expectedTokensBySoul.tokens.length; i++) {
      const elemSlot = valuesBase + BigInt(i);
      const rawTokenO = await originalProvider.getStorageAt(config.soulBoundTokenRegistry.original, slotToHex32(elemSlot), originalBlockTag);
      const rawTokenT = await targetProvider.getStorageAt(config.soulBoundTokenRegistry.target, slotToHex32(elemSlot), targetBlockTag);
      const decodedO = decodeTokenBytes32(rawTokenO);
      const decodedT = decodeTokenBytes32(rawTokenT);

      const expectedTok = expectedTokensBySoul.tokens[i];
      const labelO = collectionLabelForAddress(config, "original", decodedO.collectionAddress);
      const labelT = collectionLabelForAddress(config, "target", decodedT.collectionAddress);

      if (labelO !== labelT || labelO !== expectedTok.collectionLabel || decodedO.tokenId !== expectedTok.tokenId || decodedT.tokenId !== expectedTok.tokenId) {
        throw new Error(`SoulBoundTokenRegistry tokensBySoul mismatch for soulId=${soul.id}, index=${i}`);
      }
    }

    if (soulIdx === 0 || soulIdx === souls.length - 1 || (soulIdx + 1) % logEvery === 0) {
      logProgress(`assert: SoulBoundTokenRegistry storage ${soulIdx + 1}/${souls.length}`);
    }
  }

  // soulsByToken storage checks can be expensive (full reverse-index validation).
  if (config.enableReverseIndexChecks) {
    const tokenKeys = originalSnapshot.soulBoundTokenRegistry.soulsByTokenKey;
    logProgress(`assert: soulsByToken storage checks tokenKeys=${tokenKeys.length}`);
    for (let tokenIdx = 0; tokenIdx < tokenKeys.length; tokenIdx++) {
      const tokenEntry = tokenKeys[tokenIdx];
      const tokenBytesO = reconstructTokenBytes32ForSide({
        config,
        side: "original",
        tokenKey: tokenEntry.tokenKey,
      });
      const tokenBytesT = reconstructTokenBytes32ForSide({
        config,
        side: "target",
        tokenKey: tokenEntry.tokenKey,
      });
      if (!tokenBytesO || !tokenBytesT) continue;

      const setEntrySlotO = mappingEntrySlot({ baseSlot: soulsByTokenBase, keyType: "bytes32", keyValue: tokenBytesO });
      const setEntrySlotT = mappingEntrySlot({ baseSlot: soulsByTokenBase, keyType: "bytes32", keyValue: tokenBytesT });

      const rawLenO = await originalProvider.getStorageAt(config.soulBoundTokenRegistry.original, slotToHex32(setEntrySlotO), originalBlockTag);
      const rawLenT = await targetProvider.getStorageAt(config.soulBoundTokenRegistry.target, slotToHex32(setEntrySlotT), targetBlockTag);
      const lenO = BigInt(rawLenO).toString();
      const lenT = BigInt(rawLenT).toString();

      const expectedSouls = tokenEntry.soulIds;
      if (lenO !== lenT || lenO !== String(expectedSouls.length)) {
        throw new Error(`SoulBoundTokenRegistry soulsByToken length mismatch for token=${tokenEntry.tokenKey.collectionLabel}:${tokenEntry.tokenKey.tokenId}`);
      }

      const valuesBaseO = dynamicArrayDataBase(setEntrySlotO);
      const valuesBaseT = dynamicArrayDataBase(setEntrySlotT);
      for (let i = 0; i < expectedSouls.length; i++) {
        const elemSlotO = valuesBaseO + BigInt(i);
        const elemSlotT = valuesBaseT + BigInt(i);
        const rawSoulO = await originalProvider.getStorageAt(config.soulBoundTokenRegistry.original, slotToHex32(elemSlotO), originalBlockTag);
        const rawSoulT = await targetProvider.getStorageAt(config.soulBoundTokenRegistry.target, slotToHex32(elemSlotT), targetBlockTag);
        const soulIdO = BigInt(rawSoulO).toString();
        const soulIdT = BigInt(rawSoulT).toString();
        if (soulIdO !== soulIdT || soulIdO !== expectedSouls[i]) {
          throw new Error(`SoulBoundTokenRegistry soulsByToken mismatch for token=${tokenEntry.tokenKey.collectionLabel}:${tokenEntry.tokenKey.tokenId}, index=${i}`);
        }
      }

      if (tokenIdx === 0 || tokenIdx === tokenKeys.length - 1 || (tokenIdx + 1) % logEvery === 0) {
        logProgress(`assert: soulsByToken ${tokenIdx + 1}/${tokenKeys.length}`);
      }
    }
  } else {
    logProgress("assert: skipping soulsByToken reverse-index storage checks (enableReverseIndexChecks=false)");
  }

  // --- SoulDrop storage checks
  const dropLayout = getStorageLayoutForContract(buildInfo, { file: "contracts/drops/SoulDrop.sol", name: "SoulDrop" }).storageLayout;
  const claimedAtBase = getVarBaseSlot(dropLayout, "claimedAtBySoul");
  const withholdingsBase = getVarBaseSlot(dropLayout, "withholdingsBySoul");
  const percentBase = getVarBaseSlot(dropLayout, "percentByHoldLevel");

  logProgress(`assert: SoulDrop storage checks souls=${souls.length}`);
  for (let soulIdx = 0; soulIdx < souls.length; soulIdx++) {
    const soul = souls[soulIdx];
    const soulId = BigInt(soul.id);

    const claimedSlot = mappingEntrySlot({ baseSlot: claimedAtBase, keyType: "uint256", keyValue: soulId });
    const withholdSlot = mappingEntrySlot({ baseSlot: withholdingsBase, keyType: "uint256", keyValue: soulId });

    const rawClaimO = await originalProvider.getStorageAt(config.soulDrop.original, slotToHex32(claimedSlot), originalBlockTag);
    const rawClaimT = await targetProvider.getStorageAt(config.soulDrop.target, slotToHex32(claimedSlot), targetBlockTag);
    const claimO = BigInt(rawClaimO).toString();
    const claimT = BigInt(rawClaimT).toString();
    if (claimO !== claimT || claimO !== originalSnapshot.soulDrop.claimedAtBySoul[soul.id]) {
      throw new Error(`SoulDrop claimedAtBySoul mismatch for soulId=${soul.id}`);
    }

    const rawWithO = await originalProvider.getStorageAt(config.soulDrop.original, slotToHex32(withholdSlot), originalBlockTag);
    const rawWithT = await targetProvider.getStorageAt(config.soulDrop.target, slotToHex32(withholdSlot), targetBlockTag);
    const withO = BigInt(rawWithO).toString();
    const withT = BigInt(rawWithT).toString();
    if (withO !== withT || withO !== originalSnapshot.soulDrop.withholdingsBySoul[soul.id]) {
      throw new Error(`SoulDrop withholdingsBySoul mismatch for soulId=${soul.id}`);
    }

    if (soulIdx === 0 || soulIdx === souls.length - 1 || (soulIdx + 1) % logEvery === 0) {
      logProgress(`assert: SoulDrop storage ${soulIdx + 1}/${souls.length}`);
    }
  }

  for (let k = 0; k <= maxHoldLevelKey; k++) {
    const slot = mappingEntrySlot({ baseSlot: percentBase, keyType: "uint8", keyValue: k });
    const rawO = await originalProvider.getStorageAt(config.soulDrop.original, slotToHex32(slot), originalBlockTag);
    const rawT = await targetProvider.getStorageAt(config.soulDrop.target, slotToHex32(slot), targetBlockTag);
    const vO = BigInt(rawO).toString();
    const vT = BigInt(rawT).toString();
    const expected = originalSnapshot.soulDrop.percentByHoldLevel[String(k)];
    if (vO !== vT || vO !== expected) {
      throw new Error(`SoulDrop percentByHoldLevel mismatch for key=${k}`);
    }
  }

  // --- HoldAmount / IsVerified storage checks (Ownable._owner)
  const holdLayout = getStorageLayoutForContract(buildInfo, { file: "contracts/attributes/HoldAmount.sol", name: "HoldAmount" }).storageLayout;
  const isVerifiedLayout = getStorageLayoutForContract(buildInfo, { file: "contracts/attributes/IsVerified.sol", name: "IsVerified" }).storageLayout;
  const holdOwnerBase = getVarBaseSlot(holdLayout, "_owner");
  const isVerifiedOwnerBase = getVarBaseSlot(isVerifiedLayout, "_owner");

  const rawHoldOwnerO = await originalProvider.getStorageAt(config.holdAmount.original, slotToHex32(holdOwnerBase), originalBlockTag);
  const rawHoldOwnerT = await targetProvider.getStorageAt(config.holdAmount.target, slotToHex32(holdOwnerBase), targetBlockTag);
  const holdOwnerO = decodeAddressFromBytes32(rawHoldOwnerO);
  const holdOwnerT = decodeAddressFromBytes32(rawHoldOwnerT);
  if (holdOwnerO !== holdOwnerT || holdOwnerO !== originalSnapshot.holdAmount.owner) {
    throw new Error("HoldAmount._owner mismatch between chains or vs snapshot");
  }

  const rawIsVerifiedOwnerO = await originalProvider.getStorageAt(config.isVerified.original, slotToHex32(isVerifiedOwnerBase), originalBlockTag);
  const rawIsVerifiedOwnerT = await targetProvider.getStorageAt(config.isVerified.target, slotToHex32(isVerifiedOwnerBase), targetBlockTag);
  const isVerifiedOwnerO = decodeAddressFromBytes32(rawIsVerifiedOwnerO);
  const isVerifiedOwnerT = decodeAddressFromBytes32(rawIsVerifiedOwnerT);
  if (isVerifiedOwnerO !== isVerifiedOwnerT || isVerifiedOwnerO !== originalSnapshot.isVerified.owner) {
    throw new Error("IsVerified._owner mismatch between chains or vs snapshot");
  }
}

