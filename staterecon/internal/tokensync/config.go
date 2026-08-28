package tokensync

import (
	"fmt"
	"strings"

	"github.com/whitechainio/whitechain-utils/staterecon/internal/models"
	"github.com/whitechainio/whitechain-utils/staterecon/internal/tokenconfig"
)

type Config = tokenconfig.Config

// LoadConfig reads and parses a YAML token config file.
func LoadConfig(path string) (*Config, error) {
	return tokenconfig.Load(path)
}

func BuildTargets(c *Config) ([]tokenTarget, error) {
	if c == nil {
		return nil, fmt.Errorf("config is nil")
	}

	sections := c.All

	targets := make([]tokenTarget, 0, totalEntries(sections))
	seen := make(map[string]tokenKind, totalEntries(sections))

	for _, section := range sections {
		kind, err := formatTokenType(section.Name)
		if err != nil {
			return nil, fmt.Errorf("map config section %q to token type: %w", section.Name, err)
		}

		for _, entry := range section.Entries {
			normalized, err := models.NormalizeAddress(entry.Address)
			if err != nil {
				return nil, err
			}

			key := strings.ToLower(normalized)
			if existing, ok := seen[key]; ok {
				if existing != kind {
					return nil, fmt.Errorf("address %s is listed in both %s and %s sections", normalized, existing, kind)
				}
				continue
			}

			seen[key] = kind
			targets = append(targets, tokenTarget{
				Address: normalized,
				Type:    kind,
				Section: section.Name,
			})
		}
	}

	return targets, nil
}

func totalEntries(sections []tokenconfig.Section) int {
	total := 0
	for _, section := range sections {
		total += len(section.Entries)
	}
	return total
}
