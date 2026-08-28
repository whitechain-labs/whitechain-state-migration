package tokensync

import (
	"fmt"
	"strings"
	"time"
)

// Options holds runtime parameters for token sync.
type Options struct {
	OldExplorerURL string
	BlockscoutDSN  string
	ConfigPath     string
	HTTPTimeout    time.Duration
	Force          bool
	SSH            SSHOptions
}

// SSHOptions holds optional SSH tunnel parameters for Blockscout Postgres access.
type SSHOptions struct {
	Host           string
	Port           int
	User           string
	PrivateKeyPath string
	TrustStorePath string
}

func (o SSHOptions) Enabled() bool {
	return strings.TrimSpace(o.Host) != "" ||
		strings.TrimSpace(o.User) != "" ||
		strings.TrimSpace(o.PrivateKeyPath) != ""
}

func (o SSHOptions) PortOrDefault() int {
	if o.Port <= 0 {
		return 22
	}
	return o.Port
}

// Summary holds aggregate sync results.
type Summary struct {
	Tokens     int
	Inserted   int
	Updated    int
	Skipped    int
	TypeCounts map[string]int
}

type tokenKind string

type tokenTarget struct {
	Address string
	Type    tokenKind
	Section string
}

type explorerToken struct {
	Type        string
	Contract    string
	Name        string
	Symbol      string
	TotalSupply string
	Decimals    int
}

type tokenRecord struct {
	Address         string
	Name            string
	Symbol          string
	TotalSupply     string
	Decimals        int
	Type            tokenKind
	ContractAddress []byte
}

type existingToken struct {
	HolderCount   int
	TransferCount int
}

type syncAction string

const (
	syncActionInsert syncAction = "insert"
	syncActionUpdate syncAction = "update"
	syncActionSkip   syncAction = "skip"
)

func (s *Summary) addType(kind tokenKind) {
	if s.TypeCounts == nil {
		s.TypeCounts = make(map[string]int)
	}
	s.TypeCounts[string(kind)]++
}

func formatTokenType(section string) (tokenKind, error) {
	normalized := normalizeTokenTypeSection(section)
	if normalized == "" {
		return "", fmt.Errorf("token section name is empty")
	}

	parts := strings.FieldsFunc(normalized, func(r rune) bool {
		return r == '-'
	})
	if len(parts) == 0 {
		return "", fmt.Errorf("token section name %q is empty after normalization", section)
	}

	segments := make([]string, 0, len(parts))
	for _, part := range parts {
		matches := tokenTypePartPattern.FindAllString(part, -1)
		if len(matches) == 0 || strings.Join(matches, "") != part {
			return "", fmt.Errorf("token section name %q contains unsupported characters", section)
		}
		for _, match := range matches {
			segments = append(segments, strings.ToUpper(match))
		}
	}

	return tokenKind(strings.Join(segments, "-")), nil
}

func normalizeTokenTypeSection(section string) string {
	normalized := strings.TrimSpace(strings.ToLower(section))
	normalized = strings.ReplaceAll(normalized, "_", "-")
	normalized = strings.ReplaceAll(normalized, " ", "-")
	normalized = strings.Trim(normalized, "-")
	for strings.Contains(normalized, "--") {
		normalized = strings.ReplaceAll(normalized, "--", "-")
	}
	return normalized
}
