package config

import (
	"fmt"
	"os"
	"time"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Server    ServerConfig    `yaml:"server"`
	Storage   StorageConfig   `yaml:"storage"`
	Ingest    IngestConfig    `yaml:"ingest"`
	Capture   CaptureConfig   `yaml:"capture"`
	Stats     StatsConfig     `yaml:"stats"`
	Dashboard DashboardConfig `yaml:"dashboard"`
	Logging   LoggingConfig   `yaml:"logging"`
}

type ServerConfig struct {
	GRPCListen string `yaml:"grpc_listen"`
}

type StorageConfig struct {
	DuckDBPath string `yaml:"duckdb_path"`
}

type IngestConfig struct {
	BatchSize       int      `yaml:"batch_size"`
	FlushInterval   Duration `yaml:"flush_interval"`
	BufferHardLimit int      `yaml:"buffer_hard_limit"`
}

type CaptureConfig struct {
	Enabled bool   `yaml:"enabled"`
	Dir     string `yaml:"dir"`
}

type StatsConfig struct {
	Listen      string `yaml:"listen"`       // "" disables the stats HTTP server
	EnablePProf bool   `yaml:"enable_pprof"` // when true, /debug/pprof/* is registered
}

type DashboardConfig struct {
	TopN     TopNConfig `yaml:"top_n"`
	Timezone string     `yaml:"timezone"` // IANA name, e.g. "Asia/Shanghai"
}

type TopNConfig struct {
	Tools  int `yaml:"tools"`
	Skills int `yaml:"skills"`
}

type LoggingConfig struct {
	Level  string `yaml:"level"`
	Format string `yaml:"format"`
}

type Duration time.Duration

func (d *Duration) UnmarshalYAML(node *yaml.Node) error {
	var s string
	if err := node.Decode(&s); err != nil {
		return err
	}
	parsed, err := time.ParseDuration(s)
	if err != nil {
		return fmt.Errorf("invalid duration %q: %w", s, err)
	}
	*d = Duration(parsed)
	return nil
}

func (d Duration) AsDuration() time.Duration { return time.Duration(d) }

func Load(path string) (Config, error) {
	var cfg Config

	data, err := os.ReadFile(path)
	if err != nil {
		return cfg, fmt.Errorf("read config file %s: %w", path, err)
	}

	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return cfg, fmt.Errorf("parse config %s: %w", path, err)
	}

	applyDefaults(&cfg)

	if err := validate(&cfg); err != nil {
		return cfg, fmt.Errorf("validate config %s: %w", path, err)
	}

	return cfg, nil
}

func applyDefaults(cfg *Config) {
	if cfg.Server.GRPCListen == "" {
		cfg.Server.GRPCListen = "0.0.0.0:4317"
	}
	if cfg.Storage.DuckDBPath == "" {
		cfg.Storage.DuckDBPath = "./data/monitor.duckdb"
	}
	if cfg.Ingest.BatchSize == 0 {
		cfg.Ingest.BatchSize = 500
	}
	if cfg.Ingest.FlushInterval == 0 {
		cfg.Ingest.FlushInterval = Duration(5 * time.Second)
	}
	if cfg.Ingest.BufferHardLimit == 0 {
		cfg.Ingest.BufferHardLimit = 50000
	}
	if cfg.Capture.Dir == "" {
		cfg.Capture.Dir = "./captured"
	}
	if cfg.Stats.Listen == "" {
		cfg.Stats.Listen = "127.0.0.1:9100"
	}
	if cfg.Dashboard.TopN.Tools == 0 {
		cfg.Dashboard.TopN.Tools = 10
	}
	if cfg.Dashboard.TopN.Skills == 0 {
		cfg.Dashboard.TopN.Skills = 10
	}
	if cfg.Dashboard.Timezone == "" {
		cfg.Dashboard.Timezone = "Asia/Shanghai"
	}
	if cfg.Logging.Level == "" {
		cfg.Logging.Level = "info"
	}
	if cfg.Logging.Format == "" {
		cfg.Logging.Format = "json"
	}
}

func validate(cfg *Config) error {
	if cfg.Server.GRPCListen == "" {
		return fmt.Errorf("server.grpc_listen is required")
	}
	if cfg.Storage.DuckDBPath == "" {
		return fmt.Errorf("storage.duckdb_path is required")
	}
	if cfg.Ingest.BatchSize <= 0 {
		return fmt.Errorf("ingest.batch_size must be > 0")
	}
	if cfg.Ingest.FlushInterval <= 0 {
		return fmt.Errorf("ingest.flush_interval must be > 0")
	}
	if cfg.Ingest.BufferHardLimit < cfg.Ingest.BatchSize {
		return fmt.Errorf("ingest.buffer_hard_limit (%d) must be >= batch_size (%d)",
			cfg.Ingest.BufferHardLimit, cfg.Ingest.BatchSize)
	}
	if cfg.Dashboard.TopN.Tools <= 0 {
		return fmt.Errorf("dashboard.top_n.tools must be > 0")
	}
	if cfg.Dashboard.TopN.Skills <= 0 {
		return fmt.Errorf("dashboard.top_n.skills must be > 0")
	}
	if _, err := time.LoadLocation(cfg.Dashboard.Timezone); err != nil {
		return fmt.Errorf("dashboard.timezone %q: %w", cfg.Dashboard.Timezone, err)
	}
	switch cfg.Logging.Level {
	case "debug", "info", "warn", "error":
	default:
		return fmt.Errorf("logging.level must be one of debug/info/warn/error, got %q", cfg.Logging.Level)
	}
	switch cfg.Logging.Format {
	case "json", "text":
	default:
		return fmt.Errorf("logging.format must be json or text, got %q", cfg.Logging.Format)
	}
	return nil
}
