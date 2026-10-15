package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

const (
	ConfigFilename         = "config.yaml"
	DefaultConcurrency     = 5
	DefaultMaxRetryTimes   = 2
	TokenPassphraseEnvVar  = "ENCRYPTION_PASSPHRASE"
	DefaultIncludeDefaults = true
)

type Config struct {
	Sources       []SourceConfig `yaml:"sources"`
	Concurrency   int            `yaml:"concurrency"`
	MaxRetryTimes int            `yaml:"max_retry_times"`
	Cron          string         `yaml:"cron,omitempty"`
}

type SourceConfig struct {
	IssuesEnabled    bool     `yaml:"issues_enabled"`
	ID               string   `yaml:"id"`
	Name             string   `yaml:"name,omitempty"`
	Platform         string   `yaml:"platform"`
	Username         string   `yaml:"username"`
	Token            string   `yaml:"token"`
	OnlyIncludeRepos []string `yaml:"only_include_repos"`
	ExcludeRepos     []string `yaml:"exclude_repos"`
	IncludeDefaults  bool     `yaml:"include_defaults"`
	IncludeStarred   bool     `yaml:"include_starred"`
	IncludeWatching  bool     `yaml:"include_watching"`
}

type LoadedConfig struct {
	Path string
	Node *yaml.Node
	Data Config
}

type SecretOptions struct {
	Passphrase string
}

func PathForDataDir(dataDir string) string {
	return filepath.Join(dataDir, ConfigFilename)
}

func Load(path string) (LoadedConfig, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		return LoadedConfig{}, fmt.Errorf("read config file: %w", err)
	}

	var document yaml.Node
	if err := yaml.Unmarshal(content, &document); err != nil {
		return LoadedConfig{}, fmt.Errorf("parse config file: %w", err)
	}

	cfg := Config{
		Concurrency:   DefaultConcurrency,
		MaxRetryTimes: DefaultMaxRetryTimes,
	}
	if err := document.Decode(&cfg); err != nil {
		return LoadedConfig{}, fmt.Errorf("decode config file: %w", err)
	}
	applyConfigDefaults(&cfg, &document)

	return LoadedConfig{
		Path: path,
		Node: &document,
		Data: cfg,
	}, nil
}

func LoadFromDataDir(dataDir string) (LoadedConfig, error) {
	return Load(PathForDataDir(dataDir))
}

func LoadResolved(path string, opts SecretOptions) (LoadedConfig, error) {
	loaded, err := Load(path)
	if err != nil {
		return LoadedConfig{}, err
	}

	resolvedData, err := resolveConfigSecrets(loaded.Data, opts)
	if err != nil {
		return LoadedConfig{}, fmt.Errorf("resolve config secrets: %w", err)
	}

	loaded.Data = resolvedData

	return loaded, nil
}

func applyConfigDefaults(cfg *Config, document *yaml.Node) {
	if cfg == nil {
		return
	}

	root := documentRoot(document)
	sourcesNode, _, _ := mappingValue(root, "sources")
	sourceNodes := sequenceItems(sourcesNode)

	for index := range cfg.Sources {
		cfg.Sources[index] = applySourceDefaults(cfg.Sources[index], sourceNodeAt(sourceNodes, index))
	}
}

func applySourceDefaults(source SourceConfig, node *yaml.Node) SourceConfig {
	if _, _, ok := mappingValue(node, "include_defaults"); !ok {
		source.IncludeDefaults = DefaultIncludeDefaults
	}

	return source
}

func EffectiveSourceName(source SourceConfig) string {
	trimmedName := strings.TrimSpace(source.Name)
	if trimmedName != "" {
		return trimmedName
	}

	return source.ID
}
