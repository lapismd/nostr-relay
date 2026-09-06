package khatru

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"fiatjaf.com/nostr"
	"fiatjaf.com/nostr/nip11"
	"fiatjaf.com/nostr/nip45/hyperloglog"
	"github.com/fasthttp/websocket"
	"github.com/stretchr/testify/require"
)

func TestParseCountRequestPreservesEveryFilter(t *testing.T) {
	request, err := parseCountRequest(`[
		"COUNT", "count-1",
		{"kinds":[1],"#p":["one"],"limit":10},
		{"kinds":[7],"#e":["two"]}
	]`)
	require.NoError(t, err)
	require.Equal(t, "count-1", request.SubscriptionID)
	require.Len(t, request.Filters, 2)
	require.Equal(t, []nostr.Kind{1}, request.Filters[0].Kinds)
	require.Equal(t, []string{"one"}, request.Filters[0].Tags["p"])
	require.Equal(t, 10, request.Filters[0].Limit)
	require.Equal(t, []nostr.Kind{7}, request.Filters[1].Kinds)
}

func TestCountResponseSerialization(t *testing.T) {
	approximate, err := json.Marshal(countResponseEnvelope{
		SubscriptionID: "count-1",
		Result:         CountResult{Count: 42, Approximate: true},
	})
	require.NoError(t, err)
	require.JSONEq(t, `["COUNT","count-1",{"count":42,"approximate":true}]`, string(approximate))

	exact, err := json.Marshal(countResponseEnvelope{
		SubscriptionID: "count-2",
		Result:         CountResult{Count: 7},
	})
	require.NoError(t, err)
	require.JSONEq(t, `["COUNT","count-2",{"count":7}]`, string(exact))
	require.NotContains(t, string(exact), "approximate")

	registers := make([]byte, 256)
	registers[3] = 5
	hll, err := json.Marshal(countResponseEnvelope{
		SubscriptionID: "count-3",
		Result:         CountResult{Count: 1, HyperLogLog: registers},
	})
	require.NoError(t, err)
	require.Contains(t, string(hll), hex.EncodeToString(registers))
}

func TestCountManyAndLegacyHooks(t *testing.T) {
	relay := NewRelay()
	relay.CountMany = func(_ context.Context, filters []nostr.Filter) (CountResult, error) {
		require.Len(t, filters, 2)
		return CountResult{Count: 9, Approximate: true}, nil
	}
	result, err := relay.executeCount(t.Context(), []nostr.Filter{{Kinds: []nostr.Kind{1}}, {Kinds: []nostr.Kind{7}}})
	require.NoError(t, err)
	require.Equal(t, CountResult{Count: 9, Approximate: true}, result)

	relay.CountMany = nil
	relay.Count = func(context.Context, nostr.Filter) (uint32, error) { return 3, nil }
	result, err = relay.executeCount(t.Context(), []nostr.Filter{{Kinds: []nostr.Kind{1}}})
	require.NoError(t, err)
	require.Equal(t, uint64(3), result.Count)

	_, err = relay.executeCount(t.Context(), []nostr.Filter{{}, {}})
	require.ErrorContains(t, err, "CountMany")
}

func TestLegacyHyperLogLogIsPreserved(t *testing.T) {
	target := strings.Repeat("a", 64)
	filter := nostr.Filter{Kinds: []nostr.Kind{7}, Tags: nostr.TagMap{"e": {target}}}
	relay := NewRelay()
	relay.CountHLL = func(_ context.Context, got nostr.Filter, offset int) (uint32, *hyperloglog.HyperLogLog, error) {
		require.Equal(t, filter, got)
		hll := hyperloglog.New(offset)
		var pubkey nostr.PubKey
		pubkey[0] = 1
		hll.Add(pubkey)
		return 1, hll, nil
	}
	result, err := relay.executeCount(t.Context(), []nostr.Filter{filter})
	require.NoError(t, err)
	require.Equal(t, uint64(1), result.Count)
	require.Len(t, result.HyperLogLog, 256)
}

func TestNIP11AdvertisesCountMany(t *testing.T) {
	relay := NewRelay()
	relay.CountMany = func(context.Context, []nostr.Filter) (CountResult, error) { return CountResult{}, nil }
	request := httptest.NewRequest(http.MethodGet, "http://relay.example", nil)
	recorder := httptest.NewRecorder()
	relay.HandleNIP11(recorder, request)

	var info nip11.RelayInformationDocument
	require.NoError(t, json.NewDecoder(recorder.Body).Decode(&info))
	require.Contains(t, info.SupportedNIPs, any(45))
}

func TestCountFailuresUseClosedAndAuthChallenge(t *testing.T) {
	relay := NewRelay()
	relay.CountMany = func(context.Context, []nostr.Filter) (CountResult, error) {
		return CountResult{}, errors.New("database unavailable")
	}
	assertCountClosed(t, relay, `["COUNT","failed",{}]`, "error: database unavailable", false)

	relay.OnCountMany = func(context.Context, []nostr.Filter) (bool, string) {
		return true, "auth-required: sign in to count"
	}
	assertCountClosed(t, relay, `["COUNT","private",{}]`, "auth-required: sign in to count", true)
}

func TestWebSocketCountManyPreservesFiltersAndApproximation(t *testing.T) {
	relay := NewRelay()
	relay.CountMany = func(_ context.Context, filters []nostr.Filter) (CountResult, error) {
		require.Len(t, filters, 2)
		require.Equal(t, []nostr.Kind{11}, filters[0].Kinds)
		require.Equal(t, []nostr.Kind{1111}, filters[1].Kinds)
		return CountResult{Count: 123_456, Approximate: true}, nil
	}
	server := httptest.NewServer(relay)
	defer server.Close()
	connection, _, err := websocket.DefaultDialer.Dial("ws"+server.URL[4:], nil)
	require.NoError(t, err)
	defer connection.Close()
	require.NoError(t, connection.WriteMessage(websocket.TextMessage, []byte(
		`["COUNT","multi",{"kinds":[11]},{"kinds":[1111]}]`,
	)))
	require.NoError(t, connection.SetReadDeadline(time.Now().Add(3*time.Second)))
	_, response, err := connection.ReadMessage()
	require.NoError(t, err)
	require.JSONEq(t, `["COUNT","multi",{"count":123456,"approximate":true}]`, string(response))
}

func assertCountClosed(t *testing.T, relay *Relay, request, expectedReason string, expectAuth bool) {
	t.Helper()
	server := httptest.NewServer(relay)
	defer server.Close()

	connection, _, err := websocket.DefaultDialer.Dial("ws"+server.URL[4:], nil)
	require.NoError(t, err)
	defer connection.Close()
	require.NoError(t, connection.WriteMessage(websocket.TextMessage, []byte(request)))
	require.NoError(t, connection.SetReadDeadline(time.Now().Add(3*time.Second)))

	sawAuth := false
	for {
		_, message, err := connection.ReadMessage()
		require.NoError(t, err)
		var envelope []json.RawMessage
		require.NoError(t, json.Unmarshal(message, &envelope))
		var label string
		require.NoError(t, json.Unmarshal(envelope[0], &label))
		if label == "AUTH" {
			sawAuth = true
			continue
		}
		require.Equal(t, "CLOSED", label)
		var reason string
		require.NoError(t, json.Unmarshal(envelope[2], &reason))
		require.Equal(t, expectedReason, reason)
		break
	}
	require.Equal(t, expectAuth, sawAuth)
}
