package artifact

import (
	"bytes"
	"fmt"
	"strings"
	"unicode/utf16"
	"unicode/utf8"

	"mcpx/internal/file"
)

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

// AlignReadWindow trims a text window to UTF-8 rune boundaries so partial
// multi-byte sequences at the end are not misclassified as binary.
func AlignReadWindow(buffer []byte) []byte {
	if len(buffer) == 0 || utf8.Valid(buffer) {
		return buffer
	}
	// Drop incomplete trailing rune.
	for len(buffer) > 0 && !utf8.Valid(buffer) {
		buffer = buffer[:len(buffer)-1]
	}
	return buffer
}
