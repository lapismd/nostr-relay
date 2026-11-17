package blossom

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"net/url"
	"syscall"
	"time"
)

const maxMirrorRequestBytes = 64 << 10

type mirrorRequest struct {
	URL string `json:"url"`
}

func handleMirror(w http.ResponseWriter, r *http.Request, options Options) {
	auth, err := authorize(r, options.ServiceURL, "upload", "", true)
	if err != nil || auth == nil {
		http.Error(w, "mirror authorization required or invalid", http.StatusUnauthorized)
		return
	}
	request, err := decodeMirrorRequest(r.Body)
	if err != nil {
		http.Error(w, "invalid mirror request", http.StatusBadRequest)
		return
	}
	client := options.MirrorClient
	if client == nil {
		client = secureMirrorClient
	}
	upstreamRequest, err := http.NewRequestWithContext(r.Context(), http.MethodGet, request.URL, nil)
	if err != nil {
		http.Error(w, "invalid mirror source", http.StatusBadRequest)
		return
	}
	response, err := client.Do(upstreamRequest)
	if err != nil {
		http.Error(w, "failed to fetch mirror source", http.StatusServiceUnavailable)
		return
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		http.Error(w, "mirror source returned a non-success status", http.StatusBadGateway)
		return
	}
	if response.ContentLength > maxUploadBytes {
		http.Error(w, "mirror source is too large", http.StatusRequestEntityTooLarge)
		return
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, maxUploadBytes+1))
	if err != nil {
		http.Error(w, "failed to read mirror source", http.StatusBadGateway)
		return
	}
	if len(body) > maxUploadBytes || (response.ContentLength >= 0 && response.ContentLength != int64(len(body))) {
		http.Error(w, "mirror source length is invalid", http.StatusRequestEntityTooLarge)
		return
	}
	digest := blobDigest(body)
	if auth.Tags.FindWithValue("x", digest) == nil {
		http.Error(w, "mirror source hash does not match authorization", http.StatusConflict)
		return
	}
	if options.StoreMirror == nil {
		http.Error(w, "mirror storage is unavailable", http.StatusServiceUnavailable)
		return
	}
	mediaType := normalizedMediaType(response.Header.Get("Content-Type"), body)
	status := http.StatusCreated
	if options.LoadBlob != nil {
		if reader, err := options.LoadBlob(digest); err == nil {
			_ = reader.Close()
			status = http.StatusOK
		}
	}
	descriptor, err := options.StoreMirror(r.Context(), *auth, body, mediaType)
	if err != nil {
		http.Error(w, "failed to store mirrored blob", http.StatusServiceUnavailable)
		return
	}
	if descriptor.SHA256 != digest || descriptor.Size != len(body) {
		http.Error(w, "stored mirror descriptor is inconsistent", http.StatusServiceUnavailable)
		return
	}
	if options.OnUpload != nil {
		if err := options.OnUpload(*auth, descriptor); err != nil {
			http.Error(w, "failed to save mirror tags", http.StatusServiceUnavailable)
			return
		}
	}
	writeEnrichedDescriptor(w, status, nil, descriptor, options)
}

func decodeMirrorRequest(body io.Reader) (mirrorRequest, error) {
	raw, err := io.ReadAll(io.LimitReader(body, maxMirrorRequestBytes+1))
	if err != nil || len(raw) > maxMirrorRequestBytes {
		return mirrorRequest{}, fmt.Errorf("mirror request is too large")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var request mirrorRequest
	if err := decoder.Decode(&request); err != nil {
		return mirrorRequest{}, err
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return mirrorRequest{}, fmt.Errorf("trailing mirror request data")
	}
	parsed, err := url.Parse(request.URL)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.User != nil {
		return mirrorRequest{}, fmt.Errorf("invalid mirror URL")
	}
	return request, nil
}

func blobDigest(body []byte) string {
	sum := sha256.Sum256(body)
	return fmt.Sprintf("%x", sum)
}

func normalizedMediaType(header string, body []byte) string {
	if parsed, _, err := mime.ParseMediaType(header); err == nil && parsed != "" {
		return parsed
	}
	if len(body) == 0 {
		return "application/octet-stream"
	}
	return http.DetectContentType(body)
}

var secureMirrorClient = &http.Client{
	Timeout: 30 * time.Second,
	Transport: &http.Transport{
		DialContext: (&net.Dialer{
			Timeout: 10 * time.Second,
			Control: func(_, address string, _ syscall.RawConn) error {
				host, _, err := net.SplitHostPort(address)
				if err != nil {
					return err
				}
				ip := net.ParseIP(host)
				if ip == nil || blockedMirrorIP(ip) {
					return fmt.Errorf("refusing mirror connection to %q", address)
				}
				return nil
			},
		}).DialContext,
	},
}

func blockedMirrorIP(ip net.IP) bool {
	return ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() ||
		ip.IsLinkLocalMulticast() || ip.IsUnspecified() || ip.IsMulticast()
}
