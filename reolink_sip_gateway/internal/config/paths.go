package config

import "path/filepath"

// StatePath keeps HA's existing paths while allowing an unprivileged native service.
func (c Config) StatePath(name string) string {
	dir := c.DataDir
	if dir == "" {
		dir = "/data"
	}
	return filepath.Join(dir, name)
}
