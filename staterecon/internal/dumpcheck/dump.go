package dumpcheck

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"os"
	"sort"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/rlp"
	"github.com/ethereum/go-ethereum/trie"
)

// zeroAddressKey is the secure trie key of the zero address. Older geth dumps write that account
// without an `address` field, only with this key.
var zeroAddressKey = crypto.Keccak256Hash(common.Address{}.Bytes())

type dumpRecord struct {
	Root     *string           `json:"root"`
	Balance  json.RawMessage   `json:"balance"`
	Nonce    *uint64           `json:"nonce"`
	CodeHash *string           `json:"codeHash"`
	Code     *string           `json:"code"`
	Storage  map[string]string `json:"storage"`
	Address  *string           `json:"address"`
	Key      *string           `json:"key"`
}

// isAccount tells an account record from the `{"root": ...}` header, which has none of these fields.
func (r *dumpRecord) isAccount() bool {
	return r.Balance != nil || r.Nonce != nil || r.CodeHash != nil || r.Code != nil ||
		r.Storage != nil || r.Address != nil || r.Key != nil
}

type keyValue struct{ key, value []byte }

// trieRoot computes the root of a trie holding the given entries.
func trieRoot(entries []keyValue) (common.Hash, error) {
	sort.Slice(entries, func(i, j int) bool { return bytes.Compare(entries[i].key, entries[j].key) < 0 })
	st := trie.NewStackTrie(nil)
	for _, e := range entries {
		if err := st.Update(e.key, e.value); err != nil {
			return common.Hash{}, err
		}
	}
	return st.Hash(), nil
}

// VerifyDump recomputes the state root of a geth JSONL dump (`geth dump`, iterative mode) and checks
// it against the dump header and, when given, the snapshot block's stateRoot. Every account's
// storage root and code hash are recomputed as well, so a skipped account, a skipped storage slot or
// altered code all fail the check. Accounts without an address (missing preimage) are counted in
// the root but cannot be migrated, so they fail the check too.
func VerifyDump(path string, expectedRoot *common.Hash, collect bool) (*DumpResult, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	res := &DumpResult{BalanceSum: new(big.Int)}
	if collect {
		res.ByAddress = make(map[common.Address]Account)
	}
	var (
		p         problems
		header    *common.Hash
		prevKey   []byte
		stack     = trie.NewStackTrie(nil)
		reader    = bufio.NewReaderSize(f, 1<<20)
		lineNo    = 0
		readError error
	)

	for readError == nil {
		var line []byte
		line, readError = reader.ReadBytes('\n')
		if readError != nil && !errors.Is(readError, io.EOF) {
			return nil, fmt.Errorf("read dump: %w", readError)
		}
		lineNo++
		line = bytes.TrimSpace(bytes.TrimPrefix(line, []byte("\xef\xbb\xbf")))
		if len(line) == 0 {
			continue
		}

		var r dumpRecord
		if err := json.Unmarshal(line, &r); err != nil {
			return nil, fmt.Errorf("dump line %d: %w", lineNo, err)
		}
		if !r.isAccount() {
			if r.Root == nil {
				return nil, fmt.Errorf("dump line %d: neither a header nor an account record", lineNo)
			}
			if header != nil {
				return nil, fmt.Errorf("dump line %d: second header line (concatenated dumps?)", lineNo)
			}
			h, err := decodeWord(*r.Root)
			if err != nil {
				return nil, fmt.Errorf("dump line %d: header root: %w", lineNo, err)
			}
			header = &h
			continue
		}
		if header == nil {
			return nil, fmt.Errorf("dump line %d: account record before the header line", lineNo)
		}

		key, addr, err := recordKey(&r)
		if err != nil {
			return nil, fmt.Errorf("dump line %d: %w", lineNo, err)
		}
		// geth writes accounts in trie order. Anything else is a duplicate or a concatenation, and the
		// root below could not be computed in one pass.
		if prevKey != nil && bytes.Compare(key, prevKey) <= 0 {
			return nil, fmt.Errorf("dump line %d: account key %x is not after the previous one (duplicate or not in trie order)", lineNo, key)
		}
		prevKey = key

		acc, accProblems, err := recordAccount(&r)
		if err != nil {
			return nil, fmt.Errorf("dump line %d: %w", lineNo, err)
		}
		for _, msg := range accProblems {
			p.add("line %d (key %x): %s", lineNo, key, msg)
		}

		enc, err := rlp.EncodeToBytes(&types.StateAccount{
			Nonce: acc.Nonce, Balance: acc.Balance, Root: acc.Root, CodeHash: acc.CodeHash.Bytes(),
		})
		if err != nil {
			return nil, fmt.Errorf("dump line %d: encode account: %w", lineNo, err)
		}
		if err := stack.Update(key, enc); err != nil {
			return nil, fmt.Errorf("dump line %d: %w", lineNo, err)
		}

		res.Accounts++
		res.BalanceSum.Add(res.BalanceSum, acc.Balance.ToBig())
		if acc.CodeHash != types.EmptyCodeHash {
			res.Contracts++
		}
		if acc.Root != types.EmptyRootHash {
			res.WithStorage++
		}
		if acc.isEmpty() {
			res.Empty++
		}

		switch {
		case addr != nil:
			if collect {
				res.ByAddress[*addr] = acc
			}
		case common.BytesToHash(key) == zeroAddressKey:
			res.ZeroAddressRestored = true
			if collect {
				res.ByAddress[common.Address{}] = acc
			}
		default:
			p.add("line %d (key %x): account without an address (missing preimage), it cannot be migrated", lineNo, key)
		}
	}

	if header == nil {
		return nil, errors.New("dump has no header line")
	}
	res.HeaderRoot = *header
	res.RecomputedRoot = stack.Hash()

	if res.Accounts == 0 {
		p.add("the dump contains no accounts")
	}
	if res.RecomputedRoot != res.HeaderRoot {
		p.add("state root recomputed from the dumped accounts %s does not match the dump header root %s: accounts are missing or altered",
			res.RecomputedRoot.Hex(), res.HeaderRoot.Hex())
	}
	if expectedRoot != nil && res.HeaderRoot != *expectedRoot {
		p.add("dump header root %s is not the snapshot block stateRoot %s: the wrong state was dumped",
			res.HeaderRoot.Hex(), expectedRoot.Hex())
	}
	return res, p.err("dump verification failed")
}

// recordKey returns the secure trie key of a dump record and its address, if it has one.
func recordKey(r *dumpRecord) ([]byte, *common.Address, error) {
	var addr *common.Address
	if r.Address != nil {
		a, err := parseAddress(*r.Address)
		if err != nil {
			return nil, nil, err
		}
		addr = &a
	}
	if r.Key == nil {
		if addr == nil {
			return nil, nil, errors.New("account record has neither an address nor a key")
		}
		return crypto.Keccak256(addr.Bytes()), addr, nil
	}
	key, err := decodeWord(*r.Key)
	if err != nil {
		return nil, nil, fmt.Errorf("key: %w", err)
	}
	if addr != nil && crypto.Keccak256Hash(addr.Bytes()) != key {
		return nil, nil, fmt.Errorf("key %s is not keccak256 of address %s", key.Hex(), addr.Hex())
	}
	return key.Bytes(), addr, nil
}

// recordAccount converts a dump record, recomputing its storage root and code hash. Mismatches are
// returned as problems; malformed fields as an error.
func recordAccount(r *dumpRecord) (Account, []string, error) {
	var msgs []string

	balance, err := parseQuantity(r.Balance)
	if err != nil {
		return Account{}, nil, fmt.Errorf("balance: %w", err)
	}
	acc := Account{Balance: balance, CodeHash: types.EmptyCodeHash, Root: types.EmptyRootHash}
	if r.Nonce != nil {
		acc.Nonce = *r.Nonce
	}
	if r.CodeHash != nil {
		if acc.CodeHash, err = decodeWord(*r.CodeHash); err != nil {
			return Account{}, nil, fmt.Errorf("codeHash: %w", err)
		}
	}
	if r.Root != nil {
		if acc.Root, err = decodeWord(*r.Root); err != nil {
			return Account{}, nil, fmt.Errorf("root: %w", err)
		}
	}

	if r.Code != nil {
		code, err := decodeHex(*r.Code)
		if err != nil {
			return Account{}, nil, fmt.Errorf("code: %w", err)
		}
		if got := crypto.Keccak256Hash(code); got != acc.CodeHash {
			msgs = append(msgs, fmt.Sprintf("code hashes to %s, record says %s", got.Hex(), acc.CodeHash.Hex()))
		}
	} else if acc.CodeHash != types.EmptyCodeHash {
		msgs = append(msgs, "code hash set but no code in the record (dump taken with --nocode?)")
	}

	slots := make([]keyValue, 0, len(r.Storage))
	for k, v := range r.Storage {
		slot, err := decodeWord(k)
		if err != nil {
			return Account{}, nil, fmt.Errorf("storage key: %w", err)
		}
		value, err := decodeHex(v)
		if err != nil {
			return Account{}, nil, fmt.Errorf("storage value: %w", err)
		}
		enc, err := rlp.EncodeToBytes(value)
		if err != nil {
			return Account{}, nil, fmt.Errorf("storage value: %w", err)
		}
		slots = append(slots, keyValue{crypto.Keccak256(slot.Bytes()), enc})
	}
	storageRoot, err := trieRoot(slots)
	if err != nil {
		return Account{}, nil, fmt.Errorf("storage: %w", err)
	}
	if storageRoot != acc.Root {
		msgs = append(msgs, fmt.Sprintf("storage recomputes to root %s, record says %s (%d slot(s) dumped): slots are missing or altered",
			storageRoot.Hex(), acc.Root.Hex(), len(slots)))
	}
	return acc, msgs, nil
}
