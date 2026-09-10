package protocol

import (
	"errors"
	"fmt"
	"time"

	"fiatjaf.com/nostr"
)

const (
	NIP44MinContentLength = 132
	NIP44MaxContentLength = 87472
	ObserverFreshness     = 5 * time.Minute
)

type ObserverDirection uint8

const (
	ObserverTelemetry ObserverDirection = iota + 1
	ObserverControl
)

type ObserverRoute struct {
	Agent     nostr.PubKey
	Owner     nostr.PubKey
	Direction ObserverDirection
	GroupID   string
}

var ErrUnknownObserverFrame = errors.New("unknown observer frame")

// ValidateObserver validates the cleartext routing envelope. The relay does
// not decrypt observer payloads; it only enforces the NIP-44 size envelope.
func ValidateObserver(event nostr.Event, now time.Time) (ObserverRoute, error) {
	if event.Kind != KindAgentObserverFrame {
		return ObserverRoute{}, errors.New("event is not an observer frame")
	}
	if len(event.Content) < NIP44MinContentLength || len(event.Content) > NIP44MaxContentLength {
		return ObserverRoute{}, errors.New("observer content must fit the NIP-44 v2 envelope")
	}
	createdAt := time.Unix(int64(event.CreatedAt), 0)
	if createdAt.Before(now.Add(-ObserverFreshness)) || createdAt.After(now.Add(ObserverFreshness)) {
		return ObserverRoute{}, errors.New("observer frame timestamp is outside the freshness window")
	}
	recipientRaw, err := singleTag(event.Tags, "p")
	if err != nil {
		return ObserverRoute{}, err
	}
	agentRaw, err := singleTag(event.Tags, "agent")
	if err != nil {
		return ObserverRoute{}, err
	}
	frame, err := singleTag(event.Tags, "frame")
	if err != nil {
		return ObserverRoute{}, err
	}
	recipient, err := nostr.PubKeyFromHex(recipientRaw)
	if err != nil {
		return ObserverRoute{}, errors.New("observer p tag must be a hex pubkey")
	}
	agent, err := nostr.PubKeyFromHex(agentRaw)
	if err != nil {
		return ObserverRoute{}, errors.New("observer agent tag must be a hex pubkey")
	}

	route := ObserverRoute{Agent: agent}
	switch {
	case event.PubKey == agent && recipient != agent:
		route.Owner = recipient
		route.Direction = ObserverTelemetry
		if frame != "telemetry" {
			return ObserverRoute{}, ErrUnknownObserverFrame
		}
	case recipient == agent && event.PubKey != agent:
		route.Owner = event.PubKey
		route.Direction = ObserverControl
		if frame != "control" {
			return ObserverRoute{}, ErrUnknownObserverFrame
		}
	default:
		return ObserverRoute{}, errors.New("observer frame must route agent-to-owner or owner-to-agent")
	}
	group, err := optionalSingleTag(event.Tags, "h")
	if err != nil {
		return ObserverRoute{}, err
	}
	route.GroupID = group
	return route, nil
}

func singleTag(tags nostr.Tags, name string) (string, error) {
	value, err := optionalSingleTag(tags, name)
	if err != nil {
		return "", err
	}
	if value == "" {
		return "", fmt.Errorf("observer frame missing %s tag", name)
	}
	return value, nil
}

func optionalSingleTag(tags nostr.Tags, name string) (string, error) {
	var value string
	count := 0
	for _, tag := range tags {
		if len(tag) == 0 || tag[0] != name {
			continue
		}
		if len(tag) != 2 || tag[1] == "" {
			return "", fmt.Errorf("observer %s tag must contain exactly one value", name)
		}
		count++
		value = tag[1]
	}
	if count > 1 {
		return "", fmt.Errorf("observer frame has multiple %s tags", name)
	}
	return value, nil
}
