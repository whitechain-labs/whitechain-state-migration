// Package dumpcheck verifies a geth JSONL state dump against its state root, and an allocation built
// from it against the dump and the base genesis, account by account.
//
// The header root of a geth dump is the root of the trie geth iterated, not a value derived from the
// accounts it wrote, so comparing it with the block's stateRoot only proves which state was dumped.
// Recomputing the root from the dumped accounts is what proves that none were skipped (geth silently
// skips accounts and storage slots whose preimage it does not have) and that none were altered.
package dumpcheck

import (
	"fmt"
	"math/big"
	"strings"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/holiman/uint256"
)

// Account is the part of an account that the state root commits to.
type Account struct {
	Balance  *uint256.Int
	Nonce    uint64
	CodeHash common.Hash
	Root     common.Hash
}

func (a Account) equal(b Account) bool {
	return a.Balance.Eq(b.Balance) && a.Nonce == b.Nonce && a.CodeHash == b.CodeHash && a.Root == b.Root
}

func (a Account) isEmpty() bool {
	return a.Balance.IsZero() && a.Nonce == 0 && a.CodeHash == types.EmptyCodeHash && a.Root == types.EmptyRootHash
}

func (a Account) String() string {
	return fmt.Sprintf("balance=%s nonce=%d codeHash=%s storageRoot=%s", a.Balance.Dec(), a.Nonce, a.CodeHash.Hex(), a.Root.Hex())
}

// DumpResult describes a verified dump.
type DumpResult struct {
	HeaderRoot          common.Hash
	RecomputedRoot      common.Hash
	Accounts            int
	Contracts           int
	WithStorage         int
	Empty               int
	ZeroAddressRestored bool
	BalanceSum          *big.Int
	// ByAddress holds every account with a known address. Only filled when requested, for the
	// allocation check.
	ByAddress map[common.Address]Account
}

// AllocResult describes a verified allocation.
type AllocResult struct {
	Entries         int
	FromDump        int // equal to the dump account (REPLACING or no collision)
	ProtectedMerged int // base contract kept, dump balance added (PROTECTED)
	KeptOverEmpty   int // base entry kept over an empty, pruned dump account
	Carried         int // base entry no dump account touched
	Pruned          int // empty dump account left out of the allocation
	AllocBalanceSum *big.Int
	DumpBalanceSum  *big.Int
	BaseRetainedSum *big.Int // balances of base entries that are still in the allocation
}

// problems collects verification failures. Format errors fail immediately instead; these are the
// findings an operator has to see in full.
type problems struct {
	list  []string
	total int
}

const maxListedProblems = 50

func (p *problems) add(format string, args ...any) {
	p.total++
	if len(p.list) < maxListedProblems {
		p.list = append(p.list, fmt.Sprintf(format, args...))
	}
}

func (p *problems) err(what string) error {
	if p.total == 0 {
		return nil
	}
	more := ""
	if p.total > len(p.list) {
		more = fmt.Sprintf("\n  ... and %d more", p.total-len(p.list))
	}
	return fmt.Errorf("%s: %d problem(s):\n  %s%s", what, p.total, strings.Join(p.list, "\n  "), more)
}
