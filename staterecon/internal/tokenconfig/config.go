package tokenconfig

import (
	"fmt"
	"os"
	"strings"

	"gopkg.in/yaml.v3"
)

// Entry holds configuration for a single token contract.
type Entry struct {
	Name      string   `yaml:"name"`
	Address   string   `yaml:"address"`
	Addresses []string `yaml:"addresses"`
	Storages  []string `yaml:"storages"`
	Rules     []string `yaml:"rules"`
}

// Config groups token entries by token standard.
type Config struct {
	ERC20  []Entry
	ERC721 []Entry
	All    []Section
}

// Section holds a raw token config section and all entries inside it.
type Section struct {
	Name    string
	Entries []Entry
}

type rawConfig struct {
	Config yaml.Node `yaml:"config"`
}

type entryList []Entry

func (l *entryList) UnmarshalYAML(value *yaml.Node) error {
	if value.Kind != yaml.SequenceNode {
		return fmt.Errorf("expected YAML sequence, got kind %d", value.Kind)
	}

	entries := make([]Entry, 0, len(value.Content))
	for _, item := range value.Content {
		switch item.Kind {
		case yaml.ScalarNode:
			var address string
			if err := item.Decode(&address); err != nil {
				return fmt.Errorf("decode token address: %w", err)
			}
			entries = append(entries, Entry{Address: address})
		case yaml.MappingNode:
			var entry Entry
			if err := item.Decode(&entry); err != nil {
				return fmt.Errorf("decode token entry: %w", err)
			}
			entries = append(entries, entry)
		default:
			return fmt.Errorf("unsupported token entry kind %d", item.Kind)
		}
	}

	*l = entries
	return nil
}

// Load reads and parses a YAML token config file.
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config %q: %w", path, err)
	}

	var raw rawConfig
	if err := yaml.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("parse config %q: %w", path, err)
	}

	return parseConfigNode(&raw.Config)
}

func parseConfigNode(node *yaml.Node) (*Config, error) {
	cfg := &Config{}
	if node == nil || (node.Kind == 0 && node.Tag == "") {
		return cfg, nil
	}
	if node.Kind != yaml.MappingNode {
		return nil, fmt.Errorf("config root must be a mapping, got kind %d", node.Kind)
	}

	cfg.All = make([]Section, 0, len(node.Content)/2)
	for i := 0; i+1 < len(node.Content); i += 2 {
		keyNode := node.Content[i]
		valueNode := node.Content[i+1]

		sectionName := strings.TrimSpace(keyNode.Value)
		if sectionName == "" {
			return nil, fmt.Errorf("config section name is empty")
		}

		var entries entryList
		if err := valueNode.Decode(&entries); err != nil {
			return nil, fmt.Errorf("decode config section %q: %w", sectionName, err)
		}

		sectionEntries := []Entry(entries)
		cfg.All = append(cfg.All, Section{
			Name:    sectionName,
			Entries: sectionEntries,
		})

		switch canonicalSectionName(sectionName) {
		case "erc20":
			cfg.ERC20 = append(cfg.ERC20, sectionEntries...)
		case "erc721":
			cfg.ERC721 = append(cfg.ERC721, sectionEntries...)
		}
	}

	return cfg, nil
}

func canonicalSectionName(name string) string {
	trimmed := strings.TrimSpace(strings.ToLower(name))
	trimmed = strings.ReplaceAll(trimmed, "_", "")
	trimmed = strings.ReplaceAll(trimmed, "-", "")
	trimmed = strings.ReplaceAll(trimmed, " ", "")
	return trimmed
}
