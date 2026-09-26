package blossom

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"mime"
	"net/http"
	"strconv"

	"fiatjaf.com/nostr"
)

const maxUploadBytes = 10 << 20

func prepareUpload(w http.ResponseWriter, r *http.Request, serviceURL, action string) *nostr.Event {
	if r.ContentLength < 0 && r.Header.Get("Content-Length") == "" {
		http.Error(w, "Content-Length required", http.StatusLengthRequired)
		return nil
	}
	if r.ContentLength > maxUploadBytes {
		http.Error(w, "file too large", http.StatusRequestEntityTooLarge)
		return nil
	}
	defer r.Body.Close()
	body, err := io.ReadAll(io.LimitReader(r.Body, maxUploadBytes+1))
	if err != nil {
		http.Error(w, "failed to read upload", http.StatusBadRequest)
		return nil
	}
	if len(body) > maxUploadBytes {
		http.Error(w, "file too large", http.StatusRequestEntityTooLarge)
		return nil
	}
	if r.ContentLength >= 0 && int64(len(body)) != r.ContentLength {
		http.Error(w, "Content-Length does not match body", http.StatusBadRequest)
		return nil
	}
	sum := sha256.Sum256(body)
	hash := hex.EncodeToString(sum[:])
	if claimed := r.Header.Get("X-SHA-256"); claimed != "" {
		if !blobHash.MatchString(claimed) {
			http.Error(w, "invalid X-SHA-256", http.StatusBadRequest)
			return nil
		}
		if claimed != hash {
			http.Error(w, "X-SHA-256 does not match body", http.StatusConflict)
			return nil
		}
	}
	auth, err := authorize(r, serviceURL, action, hash, true)
	if err != nil || auth == nil {
		http.Error(w, "upload authorization required or invalid", http.StatusUnauthorized)
		return nil
	}
	r.Body = io.NopCloser(bytes.NewReader(body))
	r.ContentLength = int64(len(body))
	r.Header.Set("Content-Length", strconv.Itoa(len(body)))
	r.Header.Set("X-SHA-256", hash)
	return auth
}

func prepareUploadCheck(w http.ResponseWriter, r *http.Request, serviceURL, action string) *nostr.Event {
	hash := r.Header.Get("X-SHA-256")
	length := r.Header.Get("X-Content-Length")
	mediaType := r.Header.Get("X-Content-Type")
	size, err := strconv.Atoi(length)
	if !blobHash.MatchString(hash) || err != nil || size <= 0 || mediaType == "" {
		http.Error(w, "invalid upload preflight headers", http.StatusBadRequest)
		return nil
	}
	if _, _, err := mime.ParseMediaType(mediaType); err != nil {
		http.Error(w, "invalid X-Content-Type", http.StatusBadRequest)
		return nil
	}
	if size > maxUploadBytes {
		http.Error(w, "file too large", http.StatusRequestEntityTooLarge)
		return nil
	}
	auth, err := authorize(r, serviceURL, action, hash, true)
	if err != nil || auth == nil {
		http.Error(w, "upload authorization required or invalid", http.StatusUnauthorized)
		return nil
	}
	return auth
}
