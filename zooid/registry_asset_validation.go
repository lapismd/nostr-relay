package zooid

import (
	"math/big"

	"fiatjaf.com/nostr"
)

type registryHeadReservation struct {
	revision *big.Int
	id       nostr.ID
}

func (p *RegistryPolicy) validateHeadAssets(event nostr.Event) string {
	if p.ValidateHeadAssets == nil {
		return ""
	}
	if err := p.ValidateHeadAssets(event); err != nil {
		return "invalid: registry assets are unavailable: " + err.Error()
	}
	return ""
}
