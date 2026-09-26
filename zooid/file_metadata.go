package zooid

import (
	"encoding/hex"
	"fmt"
	"mime"
	"net/url"
	"strconv"
	"strings"

	"fiatjaf.com/nostr"
	"zooid/internal/khatru"
)

const kindFileMetadata nostr.Kind = 1063

func advertiseNIP94(relay *khatru.Relay) {
	relay.Info.AddSupportedNIP("94")
}

func validateNIP94Event(event nostr.Event) error {
	if event.Kind != kindFileMetadata {
		return nil
	}
	fileURL, err := requiredNIP94Tag(event.Tags, "url")
	if err != nil {
		return err
	}
	parsedURL, err := url.Parse(fileURL)
	if err != nil || !parsedURL.IsAbs() {
		return fmt.Errorf("NIP-94 url must be an absolute URI")
	}
	mediaType, err := requiredNIP94Tag(event.Tags, "m")
	if err != nil {
		return err
	}
	parsedMediaType, _, err := mime.ParseMediaType(mediaType)
	if err != nil || parsedMediaType != mediaType || strings.ToLower(mediaType) != mediaType {
		return fmt.Errorf("NIP-94 m must be a lowercase MIME type without parameters")
	}
	digest, err := requiredNIP94Tag(event.Tags, "x")
	if err != nil {
		return err
	}
	if !validNIP94Digest(digest) {
		return fmt.Errorf("NIP-94 x must be a lowercase SHA-256 digest")
	}
	if original, present, err := optionalNIP94Tag(event.Tags, "ox"); err != nil {
		return err
	} else if present && !validNIP94Digest(original) {
		return fmt.Errorf("NIP-94 ox must be a lowercase SHA-256 digest")
	}
	if size, present, err := optionalNIP94Tag(event.Tags, "size"); err != nil {
		return err
	} else if present {
		if _, err := strconv.ParseUint(size, 10, 63); err != nil {
			return fmt.Errorf("NIP-94 size must be a non-negative decimal byte count")
		}
	}
	if dimensions, present, err := optionalNIP94Tag(event.Tags, "dim"); err != nil {
		return err
	} else if present && !validNIP94Dimensions(dimensions) {
		return fmt.Errorf("NIP-94 dim must contain positive width and height")
	}
	return nil
}

func requiredNIP94Tag(tags nostr.Tags, name string) (string, error) {
	value, present, err := optionalNIP94Tag(tags, name)
	if err != nil {
		return "", err
	}
	if !present || value == "" {
		return "", fmt.Errorf("NIP-94 %s tag is required", name)
	}
	return value, nil
}

func optionalNIP94Tag(tags nostr.Tags, name string) (string, bool, error) {
	value := ""
	count := 0
	for _, tag := range tags {
		if len(tag) >= 2 && tag[0] == name {
			value, count = tag[1], count+1
		}
	}
	if count > 1 {
		return "", false, fmt.Errorf("NIP-94 %s tag must not be repeated", name)
	}
	return value, count == 1, nil
}

func validNIP94Digest(value string) bool {
	if len(value) != 64 || strings.ToLower(value) != value {
		return false
	}
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == 32
}

func validNIP94Dimensions(value string) bool {
	parts := strings.Split(value, "x")
	if len(parts) != 2 {
		return false
	}
	width, widthErr := strconv.ParseUint(parts[0], 10, 31)
	height, heightErr := strconv.ParseUint(parts[1], 10, 31)
	return widthErr == nil && heightErr == nil && width > 0 && height > 0
}
