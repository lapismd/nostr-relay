package blossom

import (
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"testing"
)

func TestOptimizeStaticImageResizesAndAppliesOrientation(t *testing.T) {
	large := image.NewNRGBA(image.Rect(0, 0, 4096, 1024))
	large.Set(0, 0, color.NRGBA{R: 255, A: 255})
	var encodedPNG bytes.Buffer
	if err := png.Encode(&encodedPNG, large); err != nil {
		t.Fatal(err)
	}
	resized, mediaType, status := optimizeStaticImage(encodedPNG.Bytes())
	if status != 0 || mediaType != "image/png" {
		t.Fatalf("PNG optimization = type %q status %d", mediaType, status)
	}
	if config, _, err := image.DecodeConfig(bytes.NewReader(resized)); err != nil || config.Width != 2048 || config.Height != 512 {
		t.Fatalf("resized dimensions = %#v error=%v", config, err)
	}

	portrait := image.NewNRGBA(image.Rect(0, 0, 2, 3))
	var encodedJPEG bytes.Buffer
	if err := jpeg.Encode(&encodedJPEG, portrait, &jpeg.Options{Quality: 90}); err != nil {
		t.Fatal(err)
	}
	orientedJPEG := addJPEGOrientation(encodedJPEG.Bytes(), 6)
	oriented, mediaType, status := optimizeStaticImage(orientedJPEG)
	if status != 0 || mediaType != "image/jpeg" {
		t.Fatalf("JPEG optimization = type %q status %d", mediaType, status)
	}
	if config, _, err := image.DecodeConfig(bytes.NewReader(oriented)); err != nil || config.Width != 3 || config.Height != 2 {
		t.Fatalf("oriented dimensions = %#v error=%v", config, err)
	}
}

func TestOptimizeStaticWebPToMetadataFreePNG(t *testing.T) {
	encoded, err := base64.StdEncoding.DecodeString("UklGRiIAAABXRUJQVlA4IBYAAAAwAQCdASoBAAEADsD+JaQAA3AAAAAA")
	if err != nil {
		t.Fatal(err)
	}
	processed, mediaType, status := optimizeStaticImage(encoded)
	if status != 0 || mediaType != "image/png" || !bytes.HasPrefix(processed, []byte("\x89PNG")) {
		t.Fatalf("WebP optimization = type %q status %d bytes=%x", mediaType, status, processed[:min(len(processed), 8)])
	}
	if !supportedStaticImage("image/webp") {
		t.Fatal("WebP preflight support is disabled")
	}
}

func addJPEGOrientation(jpegBytes []byte, orientation uint16) []byte {
	tiff := make([]byte, 26)
	copy(tiff[:2], "II")
	binary.LittleEndian.PutUint16(tiff[2:4], 42)
	binary.LittleEndian.PutUint32(tiff[4:8], 8)
	binary.LittleEndian.PutUint16(tiff[8:10], 1)
	binary.LittleEndian.PutUint16(tiff[10:12], 0x0112)
	binary.LittleEndian.PutUint16(tiff[12:14], 3)
	binary.LittleEndian.PutUint32(tiff[14:18], 1)
	binary.LittleEndian.PutUint16(tiff[18:20], orientation)
	payload := append([]byte("Exif\x00\x00"), tiff...)
	segment := []byte{0xff, 0xe1, 0, 0}
	binary.BigEndian.PutUint16(segment[2:4], uint16(len(payload)+2))
	result := append([]byte(nil), jpegBytes[:2]...)
	result = append(result, segment...)
	result = append(result, payload...)
	return append(result, jpegBytes[2:]...)
}
