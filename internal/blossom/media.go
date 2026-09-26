package blossom

import (
	"bytes"
	"image"
	"image/jpeg"
	"image/png"
	"io"
	"net/http"
)

const maxMediaPixels = 40_000_000

func handleMediaCheck(w http.ResponseWriter, r *http.Request, options Options) {
	auth := prepareUploadCheck(w, r, options.ServiceURL, "media")
	if auth == nil {
		return
	}
	if options.CanMedia != nil && !options.CanMedia(auth.PubKey) {
		http.Error(w, "media upload forbidden", http.StatusForbidden)
		return
	}
	if !supportedStaticImage(r.Header.Get("X-Content-Type")) {
		http.Error(w, "only JPEG and PNG images are supported", http.StatusUnsupportedMediaType)
		return
	}
	w.WriteHeader(http.StatusOK)
}

func handleMedia(w http.ResponseWriter, r *http.Request, options Options) {
	auth := prepareUpload(w, r, options.ServiceURL, "media")
	if auth == nil {
		return
	}
	if options.CanMedia != nil && !options.CanMedia(auth.PubKey) {
		http.Error(w, "media upload forbidden", http.StatusForbidden)
		return
	}
	if options.StoreMedia == nil {
		http.Error(w, "media processing unavailable", http.StatusServiceUnavailable)
		return
	}
	original, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "failed to read media", http.StatusBadRequest)
		return
	}
	processed, mediaType, status := optimizeStaticImage(original)
	if status != 0 {
		http.Error(w, "only valid JPEG and PNG images are supported", status)
		return
	}
	descriptor, err := options.StoreMedia(r.Context(), *auth, processed, mediaType)
	if err != nil {
		http.Error(w, "failed to store media", http.StatusServiceUnavailable)
		return
	}
	if options.OnUpload != nil {
		if err := options.OnUpload(*auth, descriptor); err != nil {
			http.Error(w, "failed to save upload tags", http.StatusServiceUnavailable)
			return
		}
	}
	writeEnrichedDescriptor(w, http.StatusCreated, nil, descriptor, options)
}

func supportedStaticImage(mediaType string) bool {
	return mediaType == "image/jpeg" || mediaType == "image/png"
}

func optimizeStaticImage(data []byte) ([]byte, string, int) {
	config, format, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil || (format != "jpeg" && format != "png") {
		return nil, "", http.StatusUnsupportedMediaType
	}
	if config.Width <= 0 || config.Height <= 0 || int64(config.Width)*int64(config.Height) > maxMediaPixels {
		return nil, "", http.StatusUnprocessableEntity
	}
	img, decodedFormat, err := image.Decode(bytes.NewReader(data))
	if err != nil || decodedFormat != format {
		return nil, "", http.StatusUnprocessableEntity
	}
	var output bytes.Buffer
	mediaType := "image/" + format
	if format == "jpeg" {
		if err = jpeg.Encode(&output, img, &jpeg.Options{Quality: 85}); err != nil {
			return nil, "", http.StatusUnprocessableEntity
		}
	} else if err = png.Encode(&output, img); err != nil {
		return nil, "", http.StatusUnprocessableEntity
	}
	if output.Len() >= len(data) {
		return data, mediaType, 0
	}
	return output.Bytes(), mediaType, 0
}
