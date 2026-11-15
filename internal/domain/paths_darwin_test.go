package domain

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// On a Mac the layout keeps the Linux paths through /private, moves RunDir to
// /var/run, and opens what a bind passes through to the user who runs Docker
// Desktop (RFC 0055 decision 16).
func TestTheDarwinLayout(t *testing.T) {
	p := DefaultPaths("demo")
	assert.Equal(t, "/etc/demo", p.EtcDir)
	assert.Equal(t, "/var/lib/demo", p.VarDir)
	assert.Equal(t, "/var/run/demo", p.RunDir)
	assert.Equal(t, "/opt/demo", p.OptDir)
	assert.Equal(t, "/var/run/demo/secrets", p.SecretsRenderDir())

	modes := map[string]uint32{}
	for _, dir := range p.ManagedDirs() {
		modes[dir.Path] = dir.Mode
	}
	for _, path := range []string{p.EtcDir, p.VarDir, p.RunDir, p.GeneratedDir()} {
		assert.Equal(t, uint32(0o755), modes[path], path)
	}
	assert.Equal(t, uint32(0o700), modes[p.SecretsRenderDir()])
	assert.Equal(t, uint32(0o700), modes[p.AgeDir()])
	assert.Equal(t, uint32(0o750), modes[p.ManagerDir()])

	under := PathsUnder("/tmp/x", "demo")
	assert.Equal(t, "/tmp/x/var/run/demo", under.RunDir)
}
