package blossom

import (
	"bytes"
	"encoding/binary"
	"image"
	"image/jpeg"
	"image/png"
	"io"
	"net/http"

	xdraw "golang.org/x/image/draw"
	_ "golang.org/x/image/webp"
)

const maxMediaPixels = 40_000_000
const maxMediaEdge = 2048

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
		http.Error(w, "only static JPEG, PNG, and WebP images are supported", http.StatusUnsupportedMediaType)
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
		http.Error(w, "only valid static JPEG, PNG, and WebP images are supported", status)
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
	writeEnrichedDescriptorWithOriginal(w, http.StatusCreated, nil, descriptor, options, r.Header.Get("X-SHA-256"))
}

func supportedStaticImage(mediaType string) bool {
	return mediaType == "image/jpeg" || mediaType == "image/png" || mediaType == "image/webp"
}

func optimizeStaticImage(data []byte) ([]byte, string, int) {
	config, format, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil || (format != "jpeg" && format != "png" && format != "webp") || (format == "webp" && animatedWebP(data)) {
		return nil, "", http.StatusUnsupportedMediaType
	}
	if config.Width <= 0 || config.Height <= 0 || int64(config.Width)*int64(config.Height) > maxMediaPixels {
		return nil, "", http.StatusUnprocessableEntity
	}
	img, decodedFormat, err := image.Decode(bytes.NewReader(data))
	if err != nil || decodedFormat != format {
		return nil, "", http.StatusUnprocessableEntity
	}
	if format == "jpeg" {
		img = orientImage(img, jpegOrientation(data))
	}
	img = resizeImage(img, maxMediaEdge)
	var output bytes.Buffer
	mediaType := "image/png"
	if format == "jpeg" {
		mediaType = "image/jpeg"
		if err = jpeg.Encode(&output, img, &jpeg.Options{Quality: 85}); err != nil {
			return nil, "", http.StatusUnprocessableEntity
		}
	} else if err = png.Encode(&output, img); err != nil {
		return nil, "", http.StatusUnprocessableEntity
	}
	return output.Bytes(), mediaType, 0
}

func resizeImage(source image.Image, maximum int) image.Image {
	bounds := source.Bounds()
	width, height := bounds.Dx(), bounds.Dy()
	if width <= maximum && height <= maximum {
		return source
	}
	targetWidth, targetHeight := width, height
	if width >= height {
		targetWidth = maximum
		targetHeight = max(1, (height*maximum+width/2)/width)
	} else {
		targetHeight = maximum
		targetWidth = max(1, (width*maximum+height/2)/height)
	}
	destination := image.NewNRGBA(image.Rect(0, 0, targetWidth, targetHeight))
	xdraw.CatmullRom.Scale(destination, destination.Bounds(), source, bounds, xdraw.Over, nil)
	return destination
}

func orientImage(source image.Image, orientation int) image.Image {
	if orientation < 2 || orientation > 8 {
		return source
	}
	bounds := source.Bounds()
	width, height := bounds.Dx(), bounds.Dy()
	destinationWidth, destinationHeight := width, height
	if orientation >= 5 {
		destinationWidth, destinationHeight = height, width
	}
	destination := image.NewNRGBA(image.Rect(0, 0, destinationWidth, destinationHeight))
	for y := range destinationHeight {
		for x := range destinationWidth {
			sourceX, sourceY := orientedSourcePoint(x, y, width, height, orientation)
			destination.Set(x, y, source.At(bounds.Min.X+sourceX, bounds.Min.Y+sourceY))
		}
	}
	return destination
}

func orientedSourcePoint(x, y, width, height, orientation int) (int, int) {
	switch orientation {
	case 2:
		return width - 1 - x, y
	case 3:
		return width - 1 - x, height - 1 - y
	case 4:
		return x, height - 1 - y
	case 5:
		return y, x
	case 6:
		return y, height - 1 - x
	case 7:
		return width - 1 - y, height - 1 - x
	case 8:
		return width - 1 - y, x
	default:
		return x, y
	}
}

func jpegOrientation(data []byte) int {
	if len(data) < 4 || data[0] != 0xff || data[1] != 0xd8 {
		return 1
	}
	for offset := 2; offset+4 <= len(data); {
		if data[offset] != 0xff {
			break
		}
		marker := data[offset+1]
		if marker == 0xd9 || marker == 0xda {
			break
		}
		length := int(binary.BigEndian.Uint16(data[offset+2 : offset+4]))
		if length < 2 || offset+2+length > len(data) {
			break
		}
		payload := data[offset+4 : offset+2+length]
		if marker == 0xe1 && len(payload) >= 14 && string(payload[:6]) == "Exif\x00\x00" {
			if orientation := tiffOrientation(payload[6:]); orientation != 1 {
				return orientation
			}
		}
		offset += 2 + length
	}
	return 1
}

func tiffOrientation(data []byte) int {
	if len(data) < 14 {
		return 1
	}
	var order binary.ByteOrder
	switch string(data[:2]) {
	case "II":
		order = binary.LittleEndian
	case "MM":
		order = binary.BigEndian
	default:
		return 1
	}
	if order.Uint16(data[2:4]) != 42 {
		return 1
	}
	offset := int(order.Uint32(data[4:8]))
	if offset < 8 || offset+2 > len(data) {
		return 1
	}
	count := int(order.Uint16(data[offset : offset+2]))
	for index := 0; index < count; index++ {
		entry := offset + 2 + index*12
		if entry+12 > len(data) {
			return 1
		}
		if order.Uint16(data[entry:entry+2]) == 0x0112 && order.Uint16(data[entry+2:entry+4]) == 3 && order.Uint32(data[entry+4:entry+8]) == 1 {
			orientation := int(order.Uint16(data[entry+8 : entry+10]))
			if orientation >= 1 && orientation <= 8 {
				return orientation
			}
		}
	}
	return 1
}

func animatedWebP(data []byte) bool {
	if len(data) < 12 || string(data[:4]) != "RIFF" || string(data[8:12]) != "WEBP" {
		return false
	}
	for offset := 12; offset+8 <= len(data); {
		kind := string(data[offset : offset+4])
		if kind == "ANIM" || kind == "ANMF" {
			return true
		}
		size := int(binary.LittleEndian.Uint32(data[offset+4 : offset+8]))
		if size < 0 || offset+8+size > len(data) {
			return false
		}
		offset += 8 + size + size%2
	}
	return false
}
