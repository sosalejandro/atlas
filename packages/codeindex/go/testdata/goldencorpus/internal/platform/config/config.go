// Package config loads the daemon's runtime configuration.
package config

import (
	"errors"
	"os"
	"strings"
)

// Config is the process-wide configuration record.
//
// Note the name: internal/persistence declares a Config too, and both
// carry a Validate method. The scanner keys symbols on
// "ReceiverType.MethodName", so the two collapse onto one SymbolID. The
// corpus keeps the collision on purpose.
type Config struct {
	Addr string
	DSN  string
}

// Load reads a config file and applies defaults for anything missing.
func Load(path string) (*Config, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	conf := &Config{Addr: strings.TrimSpace(string(raw))}
	if isBlank(conf.Addr) {
		conf.Addr = defaultAddr()
	}
	return conf, conf.Validate()
}

// Validate rejects a Config the daemon could not start from.
func (c *Config) Validate() error {
	if isBlank(c.Addr) {
		return errors.New("config: addr is required")
	}
	return nil
}

// isBlank is an unexported plain function: the scanner drops these, so it
// never appears in the golden snapshot even though Load and Validate both
// call it.
func isBlank(s string) bool {
	return strings.TrimSpace(s) == ""
}

func defaultAddr() string {
	return ":8080"
}
