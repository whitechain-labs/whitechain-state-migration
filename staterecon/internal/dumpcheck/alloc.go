package dumpcheck

import (
	"bufio"
	"encoding/json"
	"fmt"
	"math/big"
	"os"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/rlp"
	"github.com/holiman/uint256"
)

type allocEntry struct {
	Balance json.RawMessage   `json:"balance"`
	Nonce   json.RawMessage   `json:"nonce"`
	Code    string            `json:"code"`
	Storage map[string]string `json:"storage"`
}

type baseEntry struct {
	acc Account
	// protected mirrors dump-to-alloc.mjs: a base entry with code or storage is an OP Stack contract
	// that an L1 account may only add its balance to.
	protected bool
}

// allocAccount converts a genesis alloc entry into the account it creates in the state.
func allocAccount(e *allocEntry) (Account, error) {
	balance, err := parseQuantity(e.Balance)
	if err != nil {
		return Account{}, fmt.Errorf("balance: %w", err)
	}
	nonce, err := parseQuantity(e.Nonce)
	if err != nil || !nonce.IsUint64() {
		return Account{}, fmt.Errorf("invalid nonce %s", e.Nonce)
	}
	acc := Account{Balance: balance, Nonce: nonce.Uint64(), CodeHash: types.EmptyCodeHash, Root: types.EmptyRootHash}

	code, err := decodeHex(e.Code)
	if err != nil {
		return Account{}, fmt.Errorf("code: %w", err)
	}
	if len(code) > 0 {
		acc.CodeHash = crypto.Keccak256Hash(code)
	}

	slots := make([]keyValue, 0, len(e.Storage))
	for k, v := range e.Storage {
		slot, err := decodeWord(k)
		if err != nil {
			return Account{}, fmt.Errorf("storage key: %w", err)
		}
		value, err := decodeWord(v)
		if err != nil {
			return Account{}, fmt.Errorf("storage value: %w", err)
		}
		// Genesis storage values are 32-byte words; the trie stores them without leading zeros and
		// does not store zero at all.
		trimmed := trimLeftZeros(value.Bytes())
		if len(trimmed) == 0 {
			continue
		}
		enc, err := rlp.EncodeToBytes(trimmed)
		if err != nil {
			return Account{}, fmt.Errorf("storage value: %w", err)
		}
		slots = append(slots, keyValue{crypto.Keccak256(slot.Bytes()), enc})
	}
	if acc.Root, err = trieRoot(slots); err != nil {
		return Account{}, fmt.Errorf("storage: %w", err)
	}
	return acc, nil
}

// loadBase reads the base genesis allocation (`alloc` or `allocs`, keys with or without 0x).
func loadBase(path string) (map[common.Address]baseEntry, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var genesis struct {
		Alloc  map[string]allocEntry `json:"alloc"`
		Allocs map[string]allocEntry `json:"allocs"`
	}
	if err := json.Unmarshal(raw, &genesis); err != nil {
		return nil, fmt.Errorf("parse base genesis: %w", err)
	}
	src := genesis.Alloc
	if src == nil {
		src = genesis.Allocs
	}
	if src == nil {
		return nil, fmt.Errorf("base genesis has no alloc")
	}

	base := make(map[common.Address]baseEntry, len(src))
	for k, e := range src {
		addr, err := parseAddress(k)
		if err != nil {
			return nil, fmt.Errorf("base genesis: %w", err)
		}
		if _, dup := base[addr]; dup {
			return nil, fmt.Errorf("base genesis defines %s twice", addr.Hex())
		}
		acc, err := allocAccount(&e)
		if err != nil {
			return nil, fmt.Errorf("base genesis %s: %w", addr.Hex(), err)
		}
		code, _ := decodeHex(e.Code)
		base[addr] = baseEntry{acc: acc, protected: len(code) > 0 || len(e.Storage) > 0}
	}
	return base, nil
}

// VerifyAlloc checks an allocation produced by dump-to-alloc.mjs, entry by entry, against the
// verified dump accounts and the base genesis. Every entry must be exactly one of:
//   - the dump account at that address (no collision, or a balance-only base entry it replaced);
//   - a protected base contract with the dump account's balance added (PROTECTED);
//   - a base entry kept over an empty dump account that was pruned;
//   - a base entry no dump account touched.
//
// Every non-empty dump account and every base entry must be present, and the balance sum of the
// allocation must be the dump sum plus the balances of the base entries that remained.
func VerifyAlloc(dump map[common.Address]Account, dumpBalanceSum *big.Int, genesisPath, allocPath string) (*AllocResult, error) {
	base, err := loadBase(genesisPath)
	if err != nil {
		return nil, err
	}
	f, err := os.Open(allocPath)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	res := &AllocResult{
		AllocBalanceSum: new(big.Int),
		DumpBalanceSum:  new(big.Int).Set(dumpBalanceSum),
		BaseRetainedSum: new(big.Int),
	}
	var p problems
	seen := make(map[common.Address]struct{}, len(dump)+len(base))

	// Streamed with a token decoder: the allocation can run to several gigabytes.
	dec := json.NewDecoder(bufio.NewReaderSize(f, 1<<20))
	if tok, err := dec.Token(); err != nil || tok != json.Delim('{') {
		return nil, fmt.Errorf("alloc: expected a JSON object")
	}
	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			return nil, fmt.Errorf("alloc: %w", err)
		}
		k, _ := tok.(string)
		addr, err := parseAddress(k)
		if err != nil {
			return nil, fmt.Errorf("alloc: %w", err)
		}
		var e allocEntry
		if err := dec.Decode(&e); err != nil {
			return nil, fmt.Errorf("alloc %s: %w", addr.Hex(), err)
		}
		got, err := allocAccount(&e)
		if err != nil {
			return nil, fmt.Errorf("alloc %s: %w", addr.Hex(), err)
		}

		res.Entries++
		res.AllocBalanceSum.Add(res.AllocBalanceSum, got.Balance.ToBig())
		if _, dup := seen[addr]; dup {
			p.add("%s: defined more than once in the allocation", addr.Hex())
			continue
		}
		seen[addr] = struct{}{}

		d, inDump := dump[addr]
		b, inBase := base[addr]
		switch {
		case inDump && inBase && d.isEmpty() && got.equal(b.acc):
			res.KeptOverEmpty++
			res.BaseRetainedSum.Add(res.BaseRetainedSum, b.acc.Balance.ToBig())
		// A protected base contract is only ever kept, never replaced by the dump account, even one
		// that happens to look identical.
		case inDump && inBase && b.protected:
			if !isProtectedMerge(got, d, b.acc) {
				p.add("%s: protected base contract not kept: allocation has %s, base genesis has %s, dump has %s",
					addr.Hex(), got, b.acc, d)
				continue
			}
			res.ProtectedMerged++
			res.BaseRetainedSum.Add(res.BaseRetainedSum, b.acc.Balance.ToBig())
		case inDump && got.equal(d):
			res.FromDump++
		case !inDump && inBase && got.equal(b.acc):
			res.Carried++
			res.BaseRetainedSum.Add(res.BaseRetainedSum, b.acc.Balance.ToBig())
		case inDump:
			p.add("%s: allocation has %s, dump has %s", addr.Hex(), got, d)
		case inBase:
			p.add("%s: allocation has %s, base genesis has %s", addr.Hex(), got, b.acc)
		default:
			p.add("%s: in the allocation but neither in the dump nor in the base genesis", addr.Hex())
		}
	}
	if tok, err := dec.Token(); err != nil || tok != json.Delim('}') {
		return nil, fmt.Errorf("alloc: unterminated JSON object")
	}

	for addr, d := range dump {
		if _, ok := seen[addr]; ok {
			continue
		}
		if d.isEmpty() {
			res.Pruned++
			continue
		}
		p.add("%s: dump account missing from the allocation (%s)", addr.Hex(), d)
	}
	for addr := range base {
		if _, ok := seen[addr]; !ok {
			p.add("%s: base genesis entry missing from the allocation", addr.Hex())
		}
	}

	expected := new(big.Int).Add(res.DumpBalanceSum, res.BaseRetainedSum)
	if res.AllocBalanceSum.Cmp(expected) != 0 {
		p.add("allocation balance sum %s wei is not dump sum %s + retained base balances %s",
			res.AllocBalanceSum, res.DumpBalanceSum, res.BaseRetainedSum)
	}
	return res, p.err("allocation verification failed")
}

// isProtectedMerge is the PROTECTED rule of dump-to-alloc.mjs: the base contract unchanged, with the
// dump balance added, and a dump account that is safe to merge (no storage; same code or none, and
// no nonce without code).
func isProtectedMerge(got, dump, base Account) bool {
	if dump.Root != types.EmptyRootHash {
		return false
	}
	if dump.CodeHash != types.EmptyCodeHash && dump.CodeHash != base.CodeHash {
		return false
	}
	if dump.CodeHash == types.EmptyCodeHash && dump.Nonce != 0 {
		return false
	}
	want := new(uint256.Int).Add(base.Balance, dump.Balance)
	return got.Balance.Eq(want) && got.Nonce == base.Nonce && got.CodeHash == base.CodeHash && got.Root == base.Root
}
