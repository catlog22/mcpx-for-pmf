package artifact

import (
	"bytes"
	"fmt"
	"strings"
	"unicode/utf16"
	"unicode/utf8"

	"mcpx/internal/file"
)

const (
	SourceEncodingUTF8    = "utf-8"
	SourceEncodingUTF16LE = "utf-16le"
	SourceEncodingUTF16BE = "utf-16be"
	SourceEncodingBinary  = "binary"
	SourceEncodingUnknown = "unknown"

	DeliveryEncodingUTF8   = "utf-8"
	DeliveryEncodingBase64 = "base64"
	DeliveryEncodingBlob   = "blob"
)

type SourceEncoding struct {
	Encoding string `json:"source_encoding"`
	BOM      string `json:"source_bom"`
}

func DetectSourceEncoding(path string, content []byte, registeredMIME string) SourceEncoding {
	format := file.DetectFormat(content)
	switch format.Charset {
	case SourceEncodingUTF8, SourceEncodingUTF16LE, SourceEncodingUTF16BE:
		return SourceEncoding{Encoding: format.Charset, BOM: format.BOM}
	}
	rawMIME := stripCharset(strings.TrimSpace(registeredMIME))
	if rawMIME == "" {
		rawMIME = stripCharset(file.DetectMIME(path, content))
	}
	if !isTextMIME(rawMIME) || looksBinary(content) {
		return SourceEncoding{Encoding: SourceEncodingBinary, BOM: format.BOM}
	}
	return SourceEncoding{Encoding: SourceEncodingUnknown, BOM: format.BOM}
}

func looksBinary(content []byte) bool {
	if len(content) == 0 {
		return false
	}
	control := 0
	for _, b := range content {
		if b == 0 {
			return true
		}
		if b < 0x09 || (b > 0x0d && b < 0x20) {
			control++
		}
	}
	return control*20 > len(content)
}

// TextPresentation is how resource/tool hosts should display artifact bytes.
type TextPresentation struct {
	// OK is true when content can be safely shown as UTF-8 text.
	OK bool
	// UTF8 is decoded text without a leading UTF-8 BOM when present.
	UTF8 []byte
	// MIME is a MIME type with charset=utf-8 when OK.
	MIME string
	// Format is the on-disk detection result (may differ from delivery charset).
	Format file.Format
	// RawMIME is the base type without forcing charset.
	RawMIME string
}

// PresentText classifies artifact bytes for Resource/tool delivery.
// UTF-8 and UTF-16 (BOM) become UTF-8 Text with charset=utf-8.
// Unknown / binary stay non-OK so callers use Blob instead of mojibake Text.
func PresentText(path string, content []byte, registeredMIME string) TextPresentation {
	format := file.DetectFormat(content)
	rawMIME := strings.TrimSpace(registeredMIME)
	if rawMIME == "" || rawMIME == "application/octet-stream" {
		rawMIME = file.DetectMIME(path, content)
	}
	rawMIME = stripCharset(rawMIME)

	out := TextPresentation{Format: format, RawMIME: rawMIME}
	utf8Body, ok := decodeToUTF8(content, format)
	if !ok {
		out.MIME = rawMIME
		return out
	}
	// Prefer text/* when content is valid text even if extension was binary-ish.
	base := rawMIME
	if !isTextMIME(base) {
		base = "text/plain"
	}
	out.OK = true
	out.UTF8 = utf8Body
	out.MIME = withCharset(base, "utf-8")
	return out
}

func decodeToUTF8(content []byte, format file.Format) ([]byte, bool) {
	switch format.Charset {
	case "utf-8":
		body := content
		if format.BOM == "utf-8" && bytes.HasPrefix(body, []byte{0xef, 0xbb, 0xbf}) {
			body = body[3:]
		}
		if !utf8.Valid(body) {
			return nil, false
		}
		return body, true
	case "utf-16le":
		body := content
		if bytes.HasPrefix(body, []byte{0xff, 0xfe}) {
			body = body[2:]
		}
		return utf16BytesToUTF8(body, false)
	case "utf-16be":
		body := content
		if bytes.HasPrefix(body, []byte{0xfe, 0xff}) {
			body = body[2:]
		}
		return utf16BytesToUTF8(body, true)
	default:
		return nil, false
	}
}

func utf16BytesToUTF8(body []byte, bigEndian bool) ([]byte, bool) {
	if len(body)%2 != 0 {
		return nil, false
	}
	u16 := make([]uint16, 0, len(body)/2)
	for i := 0; i+1 < len(body); i += 2 {
		var unit uint16
		if bigEndian {
			unit = uint16(body[i])<<8 | uint16(body[i+1])
		} else {
			unit = uint16(body[i+1])<<8 | uint16(body[i])
		}
		u16 = append(u16, unit)
	}
	runes := utf16.Decode(u16)
	var b strings.Builder
	b.Grow(len(runes) * 3)
	for _, r := range runes {
		if r == utf8.RuneError {
			// utf16.Decode uses U+FFFD for bad sequences; still valid UTF-8 text.
		}
		b.WriteRune(r)
	}
	out := []byte(b.String())
	if !utf8.Valid(out) {
		return nil, false
	}
	return out, true
}

func isTextMIME(value string) bool {
	value = strings.ToLower(stripCharset(value))
	return strings.HasPrefix(value, "text/") ||
		strings.Contains(value, "json") ||
		strings.Contains(value, "xml") ||
		strings.Contains(value, "yaml") ||
		strings.Contains(value, "javascript") ||
		strings.Contains(value, "typescript") ||
		value == "application/x-sh" ||
		value == "application/sql"
}

func stripCharset(mimeType string) string {
	if i := strings.IndexByte(mimeType, ';'); i >= 0 {
		return strings.TrimSpace(mimeType[:i])
	}
	return strings.TrimSpace(mimeType)
}

func withCharset(mimeType, charset string) string {
	base := stripCharset(mimeType)
	if base == "" {
		base = "text/plain"
	}
	if charset == "" {
		return base
	}
	return fmt.Sprintf("%s; charset=%s", base, charset)
}

func DeliveryMIME(mimeType, sourceEncoding string) string {
	base := stripCharset(mimeType)
	switch sourceEncoding {
	case SourceEncodingUTF8, SourceEncodingUTF16LE, SourceEncodingUTF16BE:
		if !isTextMIME(base) {
			base = "text/plain"
		}
		return withCharset(base, "utf-8")
	default:
		if base == "" {
			return "application/octet-stream"
		}
		return base
	}
}

func DecodeSourceWindow(content []byte, sourceOffset int64, source SourceEncoding) ([]byte, bool) {
	switch source.Encoding {
	case SourceEncodingUTF8:
		body := content
		if sourceOffset == 0 && source.BOM == "utf-8" && bytes.HasPrefix(body, []byte{0xef, 0xbb, 0xbf}) {
			body = body[3:]
		}
		if !utf8.Valid(body) {
			return nil, false
		}
		return body, true
	case SourceEncodingUTF16LE:
		body := content
		if sourceOffset == 0 && source.BOM == "utf-16le" && bytes.HasPrefix(body, []byte{0xff, 0xfe}) {
			body = body[2:]
		}
		return utf16BytesToUTF8(body, false)
	case SourceEncodingUTF16BE:
		body := content
		if sourceOffset == 0 && source.BOM == "utf-16be" && bytes.HasPrefix(body, []byte{0xfe, 0xff}) {
			body = body[2:]
		}
		return utf16BytesToUTF8(body, true)
	default:
		return nil, false
	}
}

func AlignSourceWindow(offset int64, limit int, size int64, source SourceEncoding) (int64, int64) {
	if offset < 0 {
		offset = 0
	}
	if offset > size {
		offset = size
	}
	if limit <= 0 {
		limit = 256 << 10
	}
	start := offset
	bomSize := int64(0)
	switch source.BOM {
	case "utf-8":
		bomSize = 3
	case "utf-16le", "utf-16be":
		bomSize = 2
	}
	if source.Encoding == SourceEncodingUTF16LE || source.Encoding == SourceEncodingUTF16BE {
		if start > 0 && start < bomSize {
			start = bomSize
		}
		if start >= bomSize && (start-bomSize)%2 != 0 {
			start++
		}
	}
	end := start + int64(limit)
	if end > size {
		end = size
	}
	if source.Encoding == SourceEncodingUTF16LE || source.Encoding == SourceEncodingUTF16BE {
		anchor := bomSize
		if end > anchor && (end-anchor)%2 != 0 {
			end--
		}
		if end <= start && start < size {
			end = start + 2
			if end > size {
				end = size
			}
		}
	}
	return start, end
}

// AlignReadWindow trims a UTF-8 text window to rune boundaries.
func AlignReadWindow(buffer []byte) []byte {
	if len(buffer) == 0 || utf8.Valid(buffer) {
		return buffer
	}
	for len(buffer) > 0 && !utf8.Valid(buffer) {
		buffer = buffer[:len(buffer)-1]
	}
	return buffer
}
