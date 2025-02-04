package runtimes

import "github.com/vito/bass/pkg/bass"

var runtimes = map[string]InitFunc{}

// InitFunc is a Runtime constructor.
type InitFunc func(bass.RuntimePool, bass.RuntimeAddrs, *bass.Scope) (bass.Runtime, error)

// Register installs a runtime under a given name.
//
// It should be called in a runtime's init() function with the runtime's
// constructor.
func RegisterRuntime(name string, init InitFunc) {
	runtimes[name] = init
}

// Init initializes the runtime registered under the given name.
func Init(config bass.RuntimeConfig, pool bass.RuntimePool) (bass.Runtime, error) {
	init, found := runtimes[config.Runtime]
	if !found {
		return nil, UnknownRuntimeError{
			Name: config.Runtime,
		}
	}

	return init(pool, config.Addrs, config.Config)
}
