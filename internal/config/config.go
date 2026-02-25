package config

import (
	"fmt"
	"os"
	"strconv"
	"time"

	"gopkg.in/yaml.v3"
)

// Config holds the application configuration
type Config struct {
	Server   ServerConfig   `yaml:"server"`
	Janus    JanusConfig    `yaml:"janus"`
	HLS      HLSConfig      `yaml:"hls"`
	Logging  LoggingConfig  `yaml:"logging"`
}

// ServerConfig holds gRPC server configuration
type ServerConfig struct {
	Port                int           `yaml:"port"`
	MaxConcurrentStreams uint32        `yaml:"max_concurrent_streams"`
	ConnectionTimeout    time.Duration `yaml:"connection_timeout"`
	EnableReflection     bool          `yaml:"enable_reflection"`
}

// JanusConfig holds default Janus Gateway configuration
type JanusConfig struct {
	GatewayAddress string `yaml:"gateway_address"`
	AdminKey       string `yaml:"admin_key"`
	AdminSecret    string `yaml:"admin_secret"`
	Timeout        int    `yaml:"timeout"`
}

// HLSConfig holds HLS output configuration
type HLSConfig struct {
	OutputDir      string `yaml:"output_dir"`
	SegmentDuration int    `yaml:"segment_duration"`
	PlaylistLength  int    `yaml:"playlist_length"`
	EnableGStreamer bool   `yaml:"enable_gstreamer"`
}

// LoggingConfig holds logging configuration
type LoggingConfig struct {
	Level  string `yaml:"level"`
	Format string `yaml:"format"`
	Output string `yaml:"output"`
}

// Default returns a configuration with default values
func Default() *Config {
	return &Config{
		Server: ServerConfig{
			Port:                50051,
			MaxConcurrentStreams: 100,
			ConnectionTimeout:    30 * time.Second,
			EnableReflection:     true,
		},
		Janus: JanusConfig{
			GatewayAddress: "ws://localhost:8188",
			AdminKey:       "adminpwd",
			AdminSecret:    "admin",
			Timeout:        10,
		},
		HLS: HLSConfig{
			OutputDir:       "/tmp/hls",
			SegmentDuration: 4,
			PlaylistLength:  5,
			EnableGStreamer: false,
		},
		Logging: LoggingConfig{
			Level:  "info",
			Format: "text",
			Output: "stdout",
		},
	}
}

// Load loads configuration from a YAML file
func Load(path string) (*Config, error) {
	cfg := Default()

	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read config file: %w", err)
	}

	if err := yaml.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("failed to parse config file: %w", err)
	}

	// Override with environment variables
	cfg.applyEnvironment()

	return cfg, nil
}

// LoadOrDefault loads config from file or returns default if file doesn't exist
func LoadOrDefault(path string) *Config {
	if path == "" {
		return Default()
	}

	cfg, err := Load(path)
	if err != nil {
		// Return default config if file doesn't exist or can't be read
		return Default()
	}

	return cfg
}

// applyEnvironment overrides configuration with environment variables
func (c *Config) applyEnvironment() {
	// Server config
	if port := os.Getenv("STREAMBRIDGE_PORT"); port != "" {
		if p, err := strconv.Atoi(port); err == nil {
			c.Server.Port = p
		}
	}
	if maxStreams := os.Getenv("STREAMBRIDGE_MAX_STREAMS"); maxStreams != "" {
		if m, err := strconv.ParseUint(maxStreams, 10, 32); err == nil {
			c.Server.MaxConcurrentStreams = uint32(m)
		}
	}
	if timeout := os.Getenv("STREAMBRIDGE_TIMEOUT"); timeout != "" {
		if t, err := time.ParseDuration(timeout); err == nil {
			c.Server.ConnectionTimeout = t
		}
	}
	if reflection := os.Getenv("STREAMBRIDGE_REFLECTION"); reflection != "" {
		if r, err := strconv.ParseBool(reflection); err == nil {
			c.Server.EnableReflection = r
		}
	}

	// Janus config
	if addr := os.Getenv("JANUS_GATEWAY_ADDRESS"); addr != "" {
		c.Janus.GatewayAddress = addr
	}
	if key := os.Getenv("JANUS_ADMIN_KEY"); key != "" {
		c.Janus.AdminKey = key
	}
	if secret := os.Getenv("JANUS_ADMIN_SECRET"); secret != "" {
		c.Janus.AdminSecret = secret
	}
	if timeout := os.Getenv("JANUS_TIMEOUT"); timeout != "" {
		if t, err := strconv.Atoi(timeout); err == nil {
			c.Janus.Timeout = t
		}
	}

	// HLS config
	if dir := os.Getenv("HLS_OUTPUT_DIR"); dir != "" {
		c.HLS.OutputDir = dir
	}
	if duration := os.Getenv("HLS_SEGMENT_DURATION"); duration != "" {
		if d, err := strconv.Atoi(duration); err == nil {
			c.HLS.SegmentDuration = d
		}
	}
	if length := os.Getenv("HLS_PLAYLIST_LENGTH"); length != "" {
		if l, err := strconv.Atoi(length); err == nil {
			c.HLS.PlaylistLength = l
		}
	}
	if gst := os.Getenv("HLS_ENABLE_GSTREAMER"); gst != "" {
		if g, err := strconv.ParseBool(gst); err == nil {
			c.HLS.EnableGStreamer = g
		}
	}

	// Logging config
	if level := os.Getenv("LOG_LEVEL"); level != "" {
		c.Logging.Level = level
	}
	if format := os.Getenv("LOG_FORMAT"); format != "" {
		c.Logging.Format = format
	}
	if output := os.Getenv("LOG_OUTPUT"); output != "" {
		c.Logging.Output = output
	}
}

// Save saves the configuration to a YAML file
func (c *Config) Save(path string) error {
	data, err := yaml.Marshal(c)
	if err != nil {
		return fmt.Errorf("failed to marshal config: %w", err)
	}

	if err := os.WriteFile(path, data, 0644); err != nil {
		return fmt.Errorf("failed to write config file: %w", err)
	}

	return nil
}

// Validate validates the configuration
func (c *Config) Validate() error {
	if c.Server.Port < 1 || c.Server.Port > 65535 {
		return fmt.Errorf("invalid server port: %d", c.Server.Port)
	}

	if c.HLS.OutputDir == "" {
		return fmt.Errorf("HLS output directory is required")
	}

	if c.HLS.SegmentDuration < 1 {
		return fmt.Errorf("HLS segment duration must be at least 1 second")
	}

	if c.HLS.PlaylistLength < 1 {
		return fmt.Errorf("HLS playlist length must be at least 1")
	}

	validLogLevels := map[string]bool{
		"debug": true, "info": true, "warn": true, "error": true,
	}
	if !validLogLevels[c.Logging.Level] {
		return fmt.Errorf("invalid log level: %s", c.Logging.Level)
	}

	return nil
}
