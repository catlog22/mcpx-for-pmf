package artifact

import (
	"strings"
	"testing"
)

func TestPresentTextUTF8AndCharset(t *testing.T) {
	content := []byte("hello 你好\n")
	pres := PresentText("notes.txt", content, "text/plain")
	if !pres.OK {
		t.Fatal("expected UTF-8 text presentation")
	}
	if string(pres.UTF8) != string(content) {
		t.Fatalf("utf8 body = %q", pres.UTF8)
	}
	if !strings.Contains(pres.MIME, "charset=utf-8") {
		t.Fatalf("mime must include charset=utf-8: %q", pres.MIME)
	}
}

func TestPresentTextUTF8BOMStripped(t *testing.T) {
	content := append([]byte{0xef, 0xbb, 0xbf}, []byte("bom-body")...)
	pres := PresentText("a.txt", content, "")
	if !pres.OK || string(pres.UTF8) != "bom-body" {
		t.Fatalf("bom strip failed: ok=%v body=%q format=%+v", pres.OK, pres.UTF8, pres.Format)
	}
	if !strings.Contains(pres.MIME, "charset=utf-8") {
		t.Fatalf("mime=%q", pres.MIME)
	}
}

func TestPresentTextUTF16LE(t *testing.T) {
	// BOM + "ok" as UTF-16LE
	content := []byte{0xff, 0xfe, 'o', 0, 'k', 0}
	pres := PresentText("a.txt", content, "text/plain")
	if !pres.OK || string(pres.UTF8) != "ok" {
		t.Fatalf("utf16le: ok=%v body=%q format=%+v", pres.OK, pres.UTF8, pres.Format)
	}
	if !strings.Contains(pres.MIME, "charset=utf-8") {
		t.Fatalf("mime=%q", pres.MIME)
	}
	if pres.Format.Charset != "utf-16le" {
		t.Fatalf("format.charset=%q", pres.Format.Charset)
	}
}

func TestPresentTextBinaryBlob(t *testing.T) {
	content := []byte{0x00, 0x01, 0xff, 0xfe, 0x80}
	pres := PresentText("blob.bin", content, "application/octet-stream")
	if pres.OK {
		t.Fatalf("binary must not be Text: %+v", pres)
	}
}
