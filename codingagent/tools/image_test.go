package tools

import (
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"image"
	"image/color"
	"strings"
	"testing"
)

const tinyPNG = "iVBORw0KGgoAAAANSUhEUgAAAAIAAAACAQMAAABIeJ9nAAAAIGNIUk0AAHomAACAhAAA+gAAAIDoAAB1MAAA6mAAADqYAAAXcJy6UTwAAAAGUExURf8AAP///0EdNBEAAAABYktHRAH/Ai3eAAAAB3RJTUUH6gEOADM5Ddoh/wAAAAxJREFUCNdjYGBgAAAABAABJzQnCgAAACV0RVh0ZGF0ZTpjcmVhdGUAMjAyNi0wMS0xNFQwMDo1MTo1NyswMDowMOnKzHgAAAAldEVYdGRhdGU6bW9kaWZ5ADIwMjYtMDEtMTRUMDA6NTE6NTcrMDA6MDCYl3TEAAAAKHRFWHRkYXRlOnRpbWVzdGFtcAAyMDI2LTAxLTE0VDAwOjUxOjU3KzAwOjAwz4JVGwAAAABJRU5ErkJggg=="
const mediumPNG100 = "iVBORw0KGgoAAAANSUhEUgAAAGQAAABkCAAAAABVicqIAAAAAmJLR0QA/4ePzL8AAAAHdElNRQfqAQ4AMzkN2iH/AAAAP0lEQVRo3u3NQQEAAAQEMASXXYrz2gqst/Lm4ZBIJBKJRCKRSCQSiUQikUgkEolEIpFIJBKJRCKRSCQSiSTsAP1cAUZeKtreAAAAJXRFWHRkYXRlOmNyZWF0ZQAyMDI2LTAxLTE0VDAwOjUxOjU3KzAwOjAw6crMeAAAACV0RVh0ZGF0ZTptb2RpZnkAMjAyNi0wMS0xNFQwMDo1MTo1NyswMDowMJiXdMQAAAAodEVYdGRhdGU6dGltZXN0YW1wADIwMjYtMDEtMTRUMDA6NTE6NTcrMDA6MDDPglUbAAAAAElFTkSuQmCC"

func mustBase64(t *testing.T, value string) []byte {
	t.Helper()
	decoded, err := base64.StdEncoding.DecodeString(value)
	if err != nil {
		t.Fatal(err)
	}
	return decoded
}

func tinyBMP() []byte {
	buffer := make([]byte, 58)
	copy(buffer, "BM")
	binary.LittleEndian.PutUint32(buffer[2:6], uint32(len(buffer)))
	binary.LittleEndian.PutUint32(buffer[10:14], 54)
	binary.LittleEndian.PutUint32(buffer[14:18], 40)
	binary.LittleEndian.PutUint32(buffer[18:22], 1)
	binary.LittleEndian.PutUint32(buffer[22:26], 1)
	binary.LittleEndian.PutUint16(buffer[26:28], 1)
	binary.LittleEndian.PutUint16(buffer[28:30], 24)
	binary.LittleEndian.PutUint32(buffer[34:38], 4)
	buffer[56] = 0xff
	return buffer
}

func TestDetectSupportedImageMimeTypeMatchesUpstreamMagic(t *testing.T) {
	if got := DetectSupportedImageMimeType(mustBase64(t, tinyPNG)); got != "image/png" {
		t.Fatalf("png = %q", got)
	}
	if got := DetectSupportedImageMimeType(tinyBMP()); got != "image/bmp" {
		t.Fatalf("bmp = %q", got)
	}
	if got := DetectSupportedImageMimeType([]byte("GIF89a")); got != "image/gif" {
		t.Fatalf("gif = %q", got)
	}
	if got := DetectSupportedImageMimeType([]byte("RIFF\x00\x00\x00\x00WEBP")); got != "image/webp" {
		t.Fatalf("webp = %q", got)
	}
	if got := DetectSupportedImageMimeType([]byte{0xff, 0xd8, 0xff, 0xf7}); got != "" {
		t.Fatalf("jpeg xl = %q", got)
	}
	apng := append([]byte(nil), mustBase64(t, tinyPNG)...)
	chunk := append([]byte{0, 0, 0, 0}, []byte("acTL")...)
	apng = append(apng[:33], append(chunk, apng[33:]...)...)
	if got := DetectSupportedImageMimeType(apng); got != "" {
		t.Fatalf("apng = %q", got)
	}
}

func TestProcessImagePassThroughResizeAndBMPConversion(t *testing.T) {
	pngBytes := mustBase64(t, tinyPNG)
	result := ProcessImage(pngBytes, "image/png", nil)
	if !result.OK || result.Data != tinyPNG || result.MimeType != "image/png" || len(result.Hints) != 0 {
		t.Fatalf("pass through = %#v", result)
	}
	resized := ResizeImage(mustBase64(t, mediumPNG100), "image/png", &ImageResizeOptions{MaxWidth: 50, MaxHeight: 50, MaxBytes: 1024 * 1024})
	if resized == nil || !resized.WasResized || resized.OriginalWidth != 100 || resized.OriginalHeight != 100 || resized.Width > 50 || resized.Height > 50 {
		t.Fatalf("resized = %#v", resized)
	}
	if note := FormatDimensionNote(*resized); !strings.Contains(note, "original 100x100") || !strings.Contains(note, "displayed at 50x50") {
		t.Fatalf("dimension note = %q", note)
	}
	autoResize := false
	bmp := ProcessImage(tinyBMP(), "image/bmp", &ProcessImageOptions{AutoResizeImages: &autoResize})
	if !bmp.OK || bmp.MimeType != "image/png" || len(bmp.Hints) != 1 || bmp.Hints[0] != "[Image converted from image/bmp to image/png.]" {
		t.Fatalf("bmp conversion = %#v", bmp)
	}
	decoded := mustBase64(t, bmp.Data)
	if !bytes.HasPrefix(decoded, []byte("\x89PNG")) {
		t.Fatalf("bmp output prefix = %x", decoded[:min(8, len(decoded))])
	}
}

func TestApplyEXIFOrientationMatrix(t *testing.T) {
	source := image.NewNRGBA(image.Rect(0, 0, 3, 2))
	values := []color.NRGBA{{R: 1, A: 255}, {R: 2, A: 255}, {R: 3, A: 255}, {R: 4, A: 255}, {R: 5, A: 255}, {R: 6, A: 255}}
	for index, value := range values {
		source.SetNRGBA(index%3, index/3, value)
	}
	want := map[int][][]uint8{
		1: {{1, 2, 3}, {4, 5, 6}},
		2: {{3, 2, 1}, {6, 5, 4}},
		3: {{6, 5, 4}, {3, 2, 1}},
		4: {{4, 5, 6}, {1, 2, 3}},
		5: {{1, 4}, {2, 5}, {3, 6}},
		6: {{4, 1}, {5, 2}, {6, 3}},
		7: {{6, 3}, {5, 2}, {4, 1}},
		8: {{3, 6}, {2, 5}, {1, 4}},
	}
	for orientation, matrix := range want {
		t.Run(string(rune('0'+orientation)), func(t *testing.T) {
			got := applyEXIFOrientation(source, orientation)
			if got.Bounds().Dx() != len(matrix[0]) || got.Bounds().Dy() != len(matrix) {
				t.Fatalf("bounds = %v", got.Bounds())
			}
			for y, row := range matrix {
				for x, expected := range row {
					red, _, _, _ := got.At(x, y).RGBA()
					if uint8(red>>8) != expected {
						t.Fatalf("pixel (%d,%d) = %d, want %d", x, y, uint8(red>>8), expected)
					}
				}
			}
		})
	}
}

func TestReadTIFFOrientationLittleAndBigEndian(t *testing.T) {
	for _, little := range []bool{true, false} {
		data := make([]byte, 26)
		var order binary.ByteOrder = binary.BigEndian
		copy(data, "MM")
		if little {
			order = binary.LittleEndian
			copy(data, "II")
		}
		order.PutUint32(data[4:8], 8)
		order.PutUint16(data[8:10], 1)
		order.PutUint16(data[10:12], 0x0112)
		order.PutUint16(data[18:20], 7)
		if got := readTIFFOrientation(data, 0); got != 7 {
			t.Fatalf("little=%v orientation=%d", little, got)
		}
	}
}
