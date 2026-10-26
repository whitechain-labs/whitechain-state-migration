package tokenrecon

import "github.com/whitechain-labs/whitechain-state-migration/staterecon/internal/tokenconfig"

type TokenConfig = tokenconfig.Entry
type Config = tokenconfig.Config

// LoadConfig reads and parses a YAML token config file.
func LoadConfig(path string) (*Config, error) {
	return tokenconfig.Load(path)
}
