package zooid

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestDispatchAcceptsConfiguredHostWithRequestPort(t *testing.T) {
	instance := &Instance{}
	instancesMux.Lock()
	previous := instancesByHost
	instancesByHost = map[string]*Instance{"local-relay.lapis.md": instance}
	instancesMux.Unlock()
	t.Cleanup(func() {
		instancesMux.Lock()
		instancesByHost = previous
		instancesMux.Unlock()
	})

	dispatched, exists := Dispatch("local-relay.lapis.md:3334")
	require.True(t, exists)
	require.Same(t, instance, dispatched)

	_, exists = Dispatch("other.example:3334")
	require.False(t, exists)
}
