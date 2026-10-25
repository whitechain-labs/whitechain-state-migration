package dumpcheck

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/rawdb"
	"github.com/ethereum/go-ethereum/core/state"
	"github.com/ethereum/go-ethereum/core/tracing"
	"github.com/ethereum/go-ethereum/triedb"
	"github.com/holiman/uint256"
)

var (
	eoa       = common.HexToAddress("0x1111111111111111111111111111111111111111")
	contract  = common.HexToAddress("0x2222222222222222222222222222222222222222")
	predeploy = common.HexToAddress("0x4200000000000000000000000000000000000007")
	dust      = common.HexToAddress("0x0000000000000000000000000000000000000001")
)

// gethDump builds a state with geth's own StateDB and dumps it with geth's own iterative dumper, so
// the expected root comes from geth, not from the code under test.
func gethDump(t *testing.T) (string, common.Hash) {
	t.Helper()
	db := state.NewDatabase(triedb.NewDatabase(rawdb.NewMemoryDatabase(), &triedb.Config{Preimages: true}), nil)
	sdb, err := state.New(common.Hash{}, db)
	if err != nil {
		t.Fatal(err)
	}
	sdb.SetBalance(eoa, uint256.NewInt(5_000_000_000_000), tracing.BalanceChangeUnspecified)
	sdb.SetNonce(eoa, 3, tracing.NonceChangeUnspecified)
	sdb.SetBalance(common.Address{}, uint256.NewInt(25_000_000_000_000_000), tracing.BalanceChangeUnspecified)
	sdb.SetNonce(contract, 1, tracing.NonceChangeUnspecified)
	sdb.SetCode(contract, []byte{0x60, 0x01, 0x60, 0x01, 0x55}, tracing.CodeChangeUnspecified)
	sdb.SetState(contract, common.HexToHash("0x01"), common.HexToHash("0x01"))
	sdb.SetState(contract, common.HexToHash("0x02"), common.HexToHash("0x0abc"))
	sdb.SetBalance(predeploy, uint256.NewInt(1), tracing.BalanceChangeUnspecified)
	root, err := sdb.Commit(0, false, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.TrieDB().Commit(root, false); err != nil {
		t.Fatal(err)
	}

	sdb, err = state.New(root, db)
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	sdb.IterativeDump(&state.DumpConfig{OnlyWithAddresses: false}, json.NewEncoder(&buf))
	return writeFile(t, "dump.jsonl", buf.String()), root
}

func writeFile(t *testing.T, name, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// editDump rewrites the account line whose JSON contains match.
func editDump(t *testing.T, path, match string, edit func(line string) string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimRight(string(raw), "\n"), "\n")
	var out []string
	found := false
	for _, l := range lines {
		if !found && strings.Contains(l, match) {
			found = true
			if l = edit(l); l == "" {
				continue
			}
		}
		out = append(out, l)
	}
	if !found {
		t.Fatalf("no dump line contains %q", match)
	}
	return writeFile(t, "edited.jsonl", strings.Join(out, "\n")+"\n")
}

func addrField(a common.Address) string {
	return fmt.Sprintf(`"address":"%s"`, strings.ToLower(a.Hex()))
}

func TestVerifyDumpMatchesGethRoot(t *testing.T) {
	path, root := gethDump(t)
	res, err := VerifyDump(path, &root, true)
	if err != nil {
		t.Fatal(err)
	}
	if res.RecomputedRoot != root || res.HeaderRoot != root {
		t.Fatalf("recomputed %s, header %s, geth %s", res.RecomputedRoot, res.HeaderRoot, root)
	}
	if res.Accounts != 4 || res.Contracts != 1 || res.WithStorage != 1 {
		t.Fatalf("unexpected counts: %+v", res)
	}
	if got := res.BalanceSum.String(); got != "25005000000000001" {
		t.Fatalf("balance sum %s", got)
	}
}

func TestVerifyDumpDetectsDamage(t *testing.T) {
	path, root := gethDump(t)
	cases := map[string]struct {
		path string
		want string
	}{
		"dropped account": {
			editDump(t, path, addrField(eoa), func(string) string { return "" }),
			"does not match the dump header root",
		},
		"dropped storage slot": {
			editDump(t, path, addrField(contract), func(l string) string {
				return strings.Replace(l, `"0x0000000000000000000000000000000000000000000000000000000000000001":"01",`, "", 1)
			}),
			"slots are missing or altered",
		},
		"altered code": {
			editDump(t, path, addrField(contract), func(l string) string { return strings.Replace(l, `"code":"0x6001`, `"code":"0x6002`, 1) }),
			"code hashes to",
		},
		"missing preimage": {
			editDump(t, path, addrField(eoa), func(l string) string { return strings.Replace(l, addrField(eoa)+",", "", 1) }),
			"missing preimage",
		},
		"duplicate account": {
			editDump(t, path, addrField(eoa), func(l string) string { return l + "\n" + l }),
			"duplicate or not in trie order",
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := VerifyDump(tc.path, &root, false)
			if err == nil {
				t.Fatal("expected an error")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error %q does not mention %q", err, tc.want)
			}
		})
	}

	headerOnly := writeFile(t, "header.jsonl", fmt.Sprintf(`{"root":"%s"}`+"\n", root.Hex()))
	if _, err := VerifyDump(headerOnly, &root, false); err == nil || !strings.Contains(err.Error(), "no accounts") {
		t.Fatalf("header-only dump: %v", err)
	}
	wrong := common.HexToHash("0x01")
	if _, err := VerifyDump(path, &wrong, false); err == nil || !strings.Contains(err.Error(), "snapshot block stateRoot") {
		t.Fatalf("wrong expected root: %v", err)
	}
}

func TestVerifyDumpRestoresZeroAddressWithoutAddressField(t *testing.T) {
	path, root := gethDump(t)
	// Older geth (the wbt fork) writes the zero address without an address field.
	edited := editDump(t, path, addrField(common.Address{}), func(l string) string {
		return strings.Replace(l, addrField(common.Address{})+",", "", 1)
	})
	res, err := VerifyDump(edited, &root, true)
	if err != nil {
		t.Fatal(err)
	}
	if !res.ZeroAddressRestored {
		t.Fatal("zero address not restored")
	}
	if _, ok := res.ByAddress[common.Address{}]; !ok {
		t.Fatal("zero address missing from the collected accounts")
	}
}

const baseGenesis = `{"alloc":{
"4200000000000000000000000000000000000007":{"balance":"0x0","nonce":"0x0","code":"0x60806040","storage":{"0x00":"0x01"}},
"0000000000000000000000000000000000000000":{"balance":"0x1"},
"0000000000000000000000000000000000000001":{"balance":"0x1"}}}`

// goodAlloc is what dump-to-alloc.mjs writes for gethDump and baseGenesis.
func goodAlloc() string {
	return `{
"0x1111111111111111111111111111111111111111":{"balance":"0x48c27395000","nonce":"0x3"},
"0x0000000000000000000000000000000000000000":{"balance":"0x58d15e17628000","nonce":"0x0"},
"0x2222222222222222222222222222222222222222":{"balance":"0x0","nonce":"0x1","code":"0x6001600155","storage":{"0x0000000000000000000000000000000000000000000000000000000000000001":"0x0000000000000000000000000000000000000000000000000000000000000001","0x0000000000000000000000000000000000000000000000000000000000000002":"0x0000000000000000000000000000000000000000000000000000000000000abc"}},
"0x4200000000000000000000000000000000000007":{"balance":"0x1","nonce":"0x0","code":"0x60806040","storage":{"0x00":"0x01"}},
"0x0000000000000000000000000000000000000001":{"balance":"0x1"}
}
`
}

func TestVerifyAlloc(t *testing.T) {
	path, root := gethDump(t)
	dump, err := VerifyDump(path, &root, true)
	if err != nil {
		t.Fatal(err)
	}
	genesis := writeFile(t, "genesis.json", baseGenesis)

	res, err := VerifyAlloc(dump.ByAddress, dump.BalanceSum, genesis, writeFile(t, "alloc.json", goodAlloc()))
	if err != nil {
		t.Fatal(err)
	}
	if res.FromDump != 3 || res.ProtectedMerged != 1 || res.Carried != 1 || res.Entries != 5 {
		t.Fatalf("unexpected counts: %+v", res)
	}

	cases := map[string]struct{ alloc, want string }{
		"predeploy overwritten by the L1 account": {
			strings.Replace(goodAlloc(), `"0x4200000000000000000000000000000000000007":{"balance":"0x1","nonce":"0x0","code":"0x60806040","storage":{"0x00":"0x01"}}`,
				`"0x4200000000000000000000000000000000000007":{"balance":"0x1","nonce":"0x0"}`, 1),
			"protected base contract not kept",
		},
		"dump account dropped": {
			strings.Replace(goodAlloc(), `"0x1111111111111111111111111111111111111111":{"balance":"0x48c27395000","nonce":"0x3"},`+"\n", "", 1),
			"dump account missing",
		},
		"balance altered": {
			strings.Replace(goodAlloc(), `"balance":"0x48c27395000"`, `"balance":"0x48c27395001"`, 1),
			"allocation has",
		},
		"zero address kept at 1 wei": {
			strings.Replace(goodAlloc(), `"balance":"0x58d15e17628000","nonce":"0x0"`, `"balance":"0x1"`, 1),
			"0x0000000000000000000000000000000000000000",
		},
		"storage slot dropped": {
			strings.Replace(goodAlloc(), `"0x0000000000000000000000000000000000000000000000000000000000000002":"0x0000000000000000000000000000000000000000000000000000000000000abc"`,
				`"0x0000000000000000000000000000000000000000000000000000000000000002":"0x0000000000000000000000000000000000000000000000000000000000000000"`, 1),
			"allocation has",
		},
		"base entry dropped": {
			strings.Replace(goodAlloc(), `,`+"\n"+`"0x0000000000000000000000000000000000000001":{"balance":"0x1"}`, "", 1),
			"base genesis entry missing",
		},
		"duplicate entry": {
			strings.Replace(goodAlloc(), "\n}\n", ",\n"+`"0x0000000000000000000000000000000000000001":{"balance":"0x2"}`+"\n}\n", 1),
			"more than once",
		},
		"extra account": {
			strings.Replace(goodAlloc(), "\n}\n", ",\n"+`"0x9999999999999999999999999999999999999999":{"balance":"0x5"}`+"\n}\n", 1),
			"neither in the dump nor in the base genesis",
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := VerifyAlloc(dump.ByAddress, dump.BalanceSum, genesis, writeFile(t, "alloc.json", tc.alloc))
			if err == nil {
				t.Fatal("expected an error")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error %q does not mention %q", err, tc.want)
			}
		})
	}
}
