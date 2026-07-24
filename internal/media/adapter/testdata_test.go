package adapter_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"hash/crc32"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"os"
	"testing"

	"github.com/ericfisherdev/nestorage/internal/media/domain"
)

// readDirNames returns the names of every file (not directory) directly
// under dir — used to assert a staging directory was left empty after a
// rejected upload.
func readDirNames(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() {
			names = append(names, e.Name())
		}
	}
	return names, nil
}

// testImage is the tiny fixture pngBytes/jpegBytes encode.
func testImage() image.Image {
	img := image.NewRGBA(image.Rect(0, 0, 2, 2))
	img.Set(0, 0, color.RGBA{R: 200, G: 100, B: 50, A: 255})
	return img
}

func pngBytes(t *testing.T) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := png.Encode(&buf, testImage()); err != nil {
		t.Fatalf("png.Encode: %v", err)
	}
	return buf.Bytes()
}

func jpegBytes(t *testing.T) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, testImage(), nil); err != nil {
		t.Fatalf("jpeg.Encode: %v", err)
	}
	return buf.Bytes()
}

// largeJPEGBytes builds a real, decodable w-by-h JPEG large enough (a few
// hundred KB or more, depending on dimensions) that reading it in a single
// buffered chunk would be obviously larger than any reasonable streaming
// chunk size — used to prove ValidateAndStage streams rather than buffers.
func largeJPEGBytes(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, color.RGBA{R: uint8(x % 256), G: uint8(y % 256), B: uint8((x + y) % 256), A: 255})
		}
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 90}); err != nil {
		t.Fatalf("jpeg.Encode: %v", err)
	}
	return buf.Bytes()
}

// decompressionBombPNGBytes hand-builds a PNG carrying an IHDR chunk that
// claims width*height pixels, followed only by an IDAT chunk HEADER (no
// payload, no IEND) — image/png's DecodeConfig reads chunks configOnly and
// returns as soon as it sees the IDAT chunk type, before reading any
// payload (verified against go/src/image/png/reader.go), so this is
// sufficient to exercise ValidateAndStage's width*height decompression-bomb
// guard without ever encoding (or decoding) width*height real pixels.
func decompressionBombPNGBytes(t *testing.T, width, height uint32) []byte {
	t.Helper()
	var buf bytes.Buffer
	buf.WriteString("\x89PNG\r\n\x1a\n")

	ihdr := make([]byte, 13)
	binary.BigEndian.PutUint32(ihdr[0:4], width)
	binary.BigEndian.PutUint32(ihdr[4:8], height)
	ihdr[8] = 8  // bit depth
	ihdr[9] = 2  // color type: truecolor
	ihdr[10] = 0 // compression method
	ihdr[11] = 0 // filter method
	ihdr[12] = 0 // interlace method
	writePNGChunk(&buf, "IHDR", ihdr)
	writePNGChunk(&buf, "IDAT", nil)

	return buf.Bytes()
}

func writePNGChunk(buf *bytes.Buffer, chunkType string, data []byte) {
	var length [4]byte
	binary.BigEndian.PutUint32(length[:], uint32(len(data)))
	buf.Write(length[:])
	buf.WriteString(chunkType)
	buf.Write(data)

	crc := crc32.NewIEEE()
	crc.Write([]byte(chunkType))
	crc.Write(data)
	var sum [4]byte
	binary.BigEndian.PutUint32(sum[:], crc.Sum32())
	buf.Write(sum[:])
}

// computeMeta builds the domain.PutMeta a PhotoService would hand to
// PhotoStore.Put for data — PhotoStore.Put trusts this completely (Sprint 5
// reconciliation R2), so PhotoStore-level tests must supply it directly
// rather than relying on Put to derive it.
func computeMeta(data []byte, contentType string) domain.PutMeta {
	sum := sha256.Sum256(data)
	return domain.PutMeta{ContentHash: hex.EncodeToString(sum[:]), SizeBytes: int64(len(data)), ContentType: contentType}
}
