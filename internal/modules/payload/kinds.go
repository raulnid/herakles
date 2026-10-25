package payload

import (
	"archive/zip"
	"bufio"
	"compress/zlib"
	"encoding/binary"
	"hash/crc32"
	"io"
)

func init() {
	Register("random", Kind{ContentType: "application/octet-stream", Ext: "bin", Write: writeRandom})
	Register("zip", Kind{ContentType: "application/zip", Ext: "zip", Write: writeZip})
	Register("png", Kind{ContentType: "image/png", Ext: "png", Write: writePNG})
}

func writeRandom(w io.Writer, size int64, rng io.Reader) error {
	_, err := io.CopyN(w, rng, size)
	return err
}

// writeZip stores (no compression) one random file of size bytes; the
// archive is that plus about 150 bytes of headers.
func writeZip(w io.Writer, size int64, rng io.Reader) error {
	zw := zip.NewWriter(w)
	f, err := zw.CreateHeader(&zip.FileHeader{Name: "payload.bin", Method: zip.Store})
	if err != nil {
		return err
	}
	if _, err := io.CopyN(f, rng, size); err != nil {
		return err
	}
	return zw.Close()
}

// writePNG streams an RGB image of random pixels with about size bytes of
// pixel data, without holding the image in memory.
func writePNG(w io.Writer, size int64, rng io.Reader) error {
	width := int64(1024)
	if px := size / 3; px < width {
		width = max(px, 1)
	}
	height := max((size/3+width-1)/width, 1)

	bw := bufio.NewWriter(w)
	if _, err := bw.WriteString("\x89PNG\r\n\x1a\n"); err != nil {
		return err
	}
	ihdr := make([]byte, 13)
	binary.BigEndian.PutUint32(ihdr[0:], uint32(width))  //nolint:gosec // width <= 1024
	binary.BigEndian.PutUint32(ihdr[4:], uint32(height)) //nolint:gosec // height <= MaxSize/3/1024
	ihdr[8], ihdr[9] = 8, 2                              // 8-bit depth, RGB; compression, filter and interlace stay 0
	if err := writeChunk(bw, "IHDR", ihdr); err != nil {
		return err
	}

	idat := &idatWriter{w: bw}
	zw, err := zlib.NewWriterLevel(idat, zlib.NoCompression)
	if err != nil {
		return err
	}
	row := make([]byte, 1+3*width) // leading 0 = filter type None
	for range height {
		if _, err := io.ReadFull(rng, row[1:]); err != nil {
			return err
		}
		if _, err := zw.Write(row); err != nil {
			return err
		}
	}
	if err := zw.Close(); err != nil {
		return err
	}
	if err := idat.flush(); err != nil {
		return err
	}
	if err := writeChunk(bw, "IEND", nil); err != nil {
		return err
	}
	return bw.Flush()
}

func writeChunk(w io.Writer, typ string, data []byte) error {
	var hdr [8]byte
	binary.BigEndian.PutUint32(hdr[:4], uint32(len(data))) //nolint:gosec // chunks are at most 64 KiB
	copy(hdr[4:], typ)
	crc := crc32.NewIEEE()
	_, _ = crc.Write(hdr[4:]) // hash writes never fail
	_, _ = crc.Write(data)
	var sum [4]byte
	binary.BigEndian.PutUint32(sum[:], crc.Sum32())
	for _, b := range [][]byte{hdr[:], data, sum[:]} {
		if _, err := w.Write(b); err != nil {
			return err
		}
	}
	return nil
}

// idatWriter splits the zlib stream into IDAT chunks of up to 64 KiB.
type idatWriter struct {
	w   io.Writer
	buf []byte
}

func (c *idatWriter) Write(p []byte) (int, error) {
	n := len(p)
	for len(p) > 0 {
		take := min(len(p), 64<<10-len(c.buf))
		c.buf = append(c.buf, p[:take]...)
		p = p[take:]
		if len(c.buf) == 64<<10 {
			if err := c.flush(); err != nil {
				return 0, err
			}
		}
	}
	return n, nil
}

func (c *idatWriter) flush() error {
	if len(c.buf) == 0 {
		return nil
	}
	err := writeChunk(c.w, "IDAT", c.buf)
	c.buf = c.buf[:0]
	return err
}
