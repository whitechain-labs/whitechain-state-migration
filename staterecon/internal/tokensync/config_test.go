package tokensync

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/whitechain-labs/whitechain-state-migration/staterecon/internal/tokenconfig"
)

func TestLoadConfigTargets(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	configPath := filepath.Join(dir, "token-config.yml")
	content := []byte(`
config:
  erc20:
    - 0x0000000000000000000000000000000000000011
    - address: 0x0000000000000000000000000000000000000022
  erc721:
    - 0x0000000000000000000000000000000000000033
  erc1155:
    - 0x0000000000000000000000000000000000000044
`)
	if err := os.WriteFile(configPath, content, 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	cfg, err := LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}

	targets, err := BuildTargets(cfg)
	if err != nil {
		t.Fatalf("BuildTargets() error = %v", err)
	}

	if len(targets) != 4 {
		t.Fatalf("Targets() len = %d, want 4", len(targets))
	}
	if targets[0].Type != tokenKind("ERC-20") ||
		targets[1].Type != tokenKind("ERC-20") ||
		targets[2].Type != tokenKind("ERC-721") ||
		targets[3].Type != tokenKind("ERC-1155") {
		t.Fatalf("unexpected target types: %#v", targets)
	}
}

func TestTargetsRejectCrossTypeDuplicates(t *testing.T) {
	t.Parallel()

	cfg := &Config{
		All: []tokenconfig.Section{
			{
				Name:    "erc20",
				Entries: []tokenconfig.Entry{{Address: "0x0000000000000000000000000000000000000011"}},
			},
			{
				Name:    "erc721",
				Entries: []tokenconfig.Entry{{Address: "0x0000000000000000000000000000000000000011"}},
			},
		},
	}

	if _, err := BuildTargets(cfg); err == nil {
		t.Fatal("BuildTargets() error = nil, want duplicate type error")
	}
}

func TestFormatTokenType(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		section string
		want    tokenKind
	}{
		{section: "erc20", want: tokenKind("ERC-20")},
		{section: "erc-721", want: tokenKind("ERC-721")},
		{section: "erc1155", want: tokenKind("ERC-1155")},
		{section: "wrc20", want: tokenKind("WRC-20")},
		{section: "my_token_404", want: tokenKind("MY-TOKEN-404")},
	}

	for _, tc := range testCases {
		tc := tc
		t.Run(tc.section, func(t *testing.T) {
			t.Parallel()

			got, err := formatTokenType(tc.section)
			if err != nil {
				t.Fatalf("formatTokenType(%q) error = %v", tc.section, err)
			}
			if got != tc.want {
				t.Fatalf("formatTokenType(%q) = %q, want %q", tc.section, got, tc.want)
			}
		})
	}
}
