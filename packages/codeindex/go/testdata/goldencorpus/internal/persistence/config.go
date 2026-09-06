package persistence

import "errors"

// Config is persistence's own configuration record. The platform config
// package declares a Config with a Validate method too, so both collapse
// onto the SymbolID "Config.Validate" — the collision is deliberate.
type Config struct {
	MaxConns int
}

// Validate rejects a pool configuration the daemon could not open.
func (c *Config) Validate() error {
	if c.MaxConns <= 0 {
		return errors.New("persistence: max_conns must be positive")
	}
	return nil
}
