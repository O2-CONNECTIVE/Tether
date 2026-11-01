//go:build !darwin

package macagent

const nativeSupported = false

func unsupported() error { return ErrUnsupported }

// The native adapter is never selected outside darwin; New returns
// ErrUnsupported before using these.
var nativeChecks = hostChecks{inheritableFDs: unsupported, childReaping: unsupported}

func nativeSpawn(spawnSpec) (spawned, error) { return spawned{}, ErrUnsupported }
