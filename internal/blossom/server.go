package blossom

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"

	"fiatjaf.com/nostr"
)

func Wrap(next http.Handler, options Options) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Expose-Headers", "Accept-Ranges, Content-Length, Content-Range, Content-Type, ETag")
		if r.Method == http.MethodOptions {
			w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type, X-SHA-256, X-Content-Type, X-Content-Length, *")
			w.Header().Set("Access-Control-Allow-Methods", "GET, HEAD, PUT, DELETE")
			w.WriteHeader(http.StatusNoContent)
			return
		}
		switch {
		case r.URL.Path == "/api/assets/reports" && r.Method == http.MethodGet:
			handleReportList(w, r, options)
		case r.URL.Path == "/report" && r.Method == http.MethodPut:
			handleReport(w, r, options.SaveReport)
		case r.URL.Path == "/upload" && r.Method == http.MethodHead:
			if prepareUploadCheck(w, r, options.ServiceURL, "upload") != nil {
				next.ServeHTTP(w, r)
			}
		case r.URL.Path == "/upload" && r.Method == http.MethodPut:
			if auth := prepareUpload(w, r, options.ServiceURL, "upload"); auth != nil {
				if options.CanUpload != nil && !options.CanUpload(auth.PubKey) {
					http.Error(w, "upload forbidden", http.StatusForbidden)
					return
				}
				if options.StoreUpload != nil {
					handlePreparedUpload(w, r, *auth, options)
				} else {
					status := http.StatusCreated
					if options.LoadBlob != nil {
						if reader, err := options.LoadBlob(r.Header.Get("X-SHA-256")); err == nil {
							_ = reader.Close()
							status = http.StatusOK
						}
					}
					enrichDescriptor(next, w, r, *auth, options, status)
				}
			}
		case r.URL.Path == "/media" && r.Method == http.MethodHead:
			handleMediaCheck(w, r, options)
		case r.URL.Path == "/media" && r.Method == http.MethodPut:
			handleMedia(w, r, options)
		case r.URL.Path == "/mirror" && r.Method == http.MethodPut:
			handleMirror(w, r, options)
		case strings.HasPrefix(r.URL.Path, "/list/") && r.Method == http.MethodGet:
			pubkey, err := nostr.PubKeyFromHex(strings.TrimPrefix(r.URL.Path, "/list/"))
			if err != nil {
				http.Error(w, "invalid list pubkey", http.StatusBadRequest)
				return
			}
			auth, err := authorize(r, options.ServiceURL, "list", "", false)
			if err != nil || auth == nil {
				http.Error(w, "list authorization required or invalid", http.StatusUnauthorized)
				return
			}
			if auth.PubKey != pubkey {
				http.Error(w, "not the blob owner", http.StatusForbidden)
				return
			}
			handleList(w, r, options, pubkey)
		case r.Method == http.MethodGet || r.Method == http.MethodHead || r.Method == http.MethodDelete:
			hash, valid := requestedHash(r.URL.Path)
			if !valid {
				next.ServeHTTP(w, r)
				return
			}
			if r.Method == http.MethodDelete {
				auth, err := authorize(r, options.ServiceURL, "delete", hash, true)
				if err != nil || auth == nil {
					http.Error(w, "delete authorization required or invalid", http.StatusUnauthorized)
					return
				}
				deleteBlob(next, w, r, *auth, hash, options.OnDelete)
				return
			} else {
				auth, err := authorize(r, options.ServiceURL, "get", hash, false)
				if err != nil {
					http.Error(w, "invalid read authorization", http.StatusUnauthorized)
					return
				}
				var pubkey *nostr.PubKey
				if auth != nil {
					pubkey = &auth.PubKey
				}
				if options.CanRead != nil && !options.CanRead(pubkey) {
					http.Error(w, "unauthorized", http.StatusForbidden)
					return
				}
				setBlobReadHeaders(w, r, hash, options)
			}
			next.ServeHTTP(w, r)
		default:
			next.ServeHTTP(w, r)
		}
	})
}

func setBlobReadHeaders(w http.ResponseWriter, r *http.Request, hash string, options Options) {
	if options.LoadBlob == nil {
		return
	}
	reader, err := options.LoadBlob(hash)
	if err != nil {
		return
	}
	_ = reader.Close()
	w.Header().Set("ETag", hash)
	w.Header().Set("Cache-Control", "public, max-age=604800, immutable")
	w.Header().Set("Accept-Ranges", "bytes")
	if options.LoadDescriptor != nil {
		if descriptor, err := options.LoadDescriptor(r.Context(), hash); err == nil && descriptor != nil {
			mediaType := descriptor.Type
			if mediaType == "" {
				mediaType = "application/octet-stream"
			}
			w.Header().Set("Content-Type", mediaType)
			w.Header().Set("Content-Length", strconv.Itoa(descriptor.Size))
		}
	}
}

func deleteBlob(next http.Handler, w http.ResponseWriter, r *http.Request, auth nostr.Event, hash string, onDelete func(nostr.Event, string) error) {
	recorded := httptest.NewRecorder()
	next.ServeHTTP(recorded, r)
	result := recorded.Result()
	defer result.Body.Close()
	if result.StatusCode >= 200 && result.StatusCode < 300 && onDelete != nil {
		if err := onDelete(auth, hash); err != nil {
			http.Error(w, "failed to update blob tags", http.StatusServiceUnavailable)
			return
		}
	}
	copyResponse(w, result)
}

func requestedHash(path string) (string, bool) {
	if len(path) < 65 || path[0] != '/' {
		return "", false
	}
	hash := path[1:65]
	if !blobHash.MatchString(hash) || (len(path) > 65 && path[65] != '.') {
		return "", false
	}
	return hash, true
}
