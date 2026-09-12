package zooid

import (
	"regexp"

	"fiatjaf.com/nostr"
)

const keytrKind = 31777

var keytrCredential = regexp.MustCompile(`^[A-Za-z0-9_-]+={0,2}$`)
var keytrRP = regexp.MustCompile(`^(?:[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?\.)*[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?$`)

// Keytr bootstraps a signer before NIP-42 is possible. Only public, encrypted,
// author-signed records receive this opt-in exception, never group events.
func validKeytrRecord(event nostr.Event) bool {
	if event.Kind != keytrKind || len(event.Content) == 0 || len(event.Content) > 4096 || !event.CheckID() || !event.VerifySignature() {
		return false
	}
	required := map[string]string{"d": "", "rp": "", "algo": "aes-256-gcm", "kdf": "hkdf-sha256", "v": "1"}
	values := make(map[string]string)
	for _, tag := range event.Tags {
		if len(tag) == 0 {
			continue
		}
		if tag[0] == "h" || tag[0] == "!" {
			return false
		}
		if expected, requiredTag := required[tag[0]]; requiredTag {
			if len(tag) != 2 || tag[1] == "" || values[tag[0]] != "" || (expected != "" && tag[1] != expected) {
				return false
			}
			values[tag[0]] = tag[1]
		}
	}
	return len(values) == len(required) && len(values["d"]) <= 1024 && len(values["d"])%4 != 1 && keytrCredential.MatchString(values["d"]) && len(values["rp"]) <= 253 && keytrRP.MatchString(values["rp"])
}
