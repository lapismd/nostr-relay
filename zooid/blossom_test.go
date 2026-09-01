package zooid

import "testing"

func TestAllowBlobRead(t *testing.T) {
	publicConfig := &Config{}
	publicConfig.Policy.Open = true
	if !allowBlobRead(publicConfig, false) {
		t.Fatal("open relay should serve public blobs without membership")
	}

	memberConfig := &Config{}
	if allowBlobRead(memberConfig, false) {
		t.Fatal("closed relay should reject a non-member blob read")
	}
	if !allowBlobRead(memberConfig, true) {
		t.Fatal("closed relay should serve blobs to relay members")
	}
}

func TestAllowBlobWrite(t *testing.T) {
	openConfig := &Config{}
	openConfig.Policy.Open = true
	if !allowBlobWrite(openConfig, false) {
		t.Fatal("open relay should accept authenticated blob writes without membership")
	}

	memberConfig := &Config{}
	if allowBlobWrite(memberConfig, false) {
		t.Fatal("closed relay should reject a non-member blob write")
	}
	if !allowBlobWrite(memberConfig, true) {
		t.Fatal("closed relay should accept blob writes from relay members")
	}
}

func TestBlossomPublicURL(t *testing.T) {
	config := &Config{Host: "relay.example"}
	if got := blossomPublicURL(config); got != "https://relay.example" {
		t.Fatalf("default public URL = %q", got)
	}

	config.Blossom.PublicURL = "http://registry.localhost:8281/"
	if got := blossomPublicURL(config); got != "http://registry.localhost:8281" {
		t.Fatalf("configured public URL = %q", got)
	}
}
