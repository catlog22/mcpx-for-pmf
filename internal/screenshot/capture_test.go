package screenshot

import (
	"context"
	"image"
	"image/color"
	"image/png"
	"os"
	"testing"
)

func TestCaptureCompressionAndResize(t *testing.T) {
	service := newService(func(_ context.Context, _ Request, path string) error {
		value := image.NewNRGBA(image.Rect(0, 0, 320, 180))
		for y := 0; y < 180; y++ {
			for x := 0; x < 320; x++ {
				value.SetNRGBA(x, y, color.NRGBA{R: uint8(x), G: uint8(y), B: 80, A: 255})
			}
		}
		handle, err := os.Create(path)
		if err != nil {
			return err
		}
		err = png.Encode(handle, value)
		closeErr := handle.Close()
		if err != nil {
			return err
		}
		return closeErr
	})
	result, err := service.Capture(context.Background(), Request{
		Mode: "region", X: -10, Y: 20, Width: 320, Height: 180,
		Compression: "small", MaxWidth: 100, MaxHeight: 100,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Metadata.MIMEType != "image/jpeg" || result.Metadata.OutputWidth != 100 || result.Metadata.OutputHeight != 56 {
		t.Fatalf("metadata: %+v", result.Metadata)
	}
	if len(result.Data) == 0 || result.Metadata.SHA256 == "" || result.Metadata.Bytes != len(result.Data) {
		t.Fatalf("encoded result: %+v", result.Metadata)
	}
}

func TestCaptureValidation(t *testing.T) {
	service := newService(func(context.Context, Request, string) error { return nil })
	for _, request := range []Request{
		{Mode: "window"},
		{Mode: "region", Width: 0, Height: 10},
		{Mode: "fullscreen", Compression: "tiny"},
		{Mode: "fullscreen", Format: "gif"},
		{Mode: "fullscreen", Format: "jpeg", Quality: 101},
	} {
		if _, err := service.Capture(context.Background(), request); err == nil {
			t.Fatalf("expected validation error for %+v", request)
		}
	}
}

func TestFitDimensionsPreservesAspectRatio(t *testing.T) {
	width, height := fitDimensions(3840, 2160, 1920, 1200)
	if width != 1920 || height != 1080 {
		t.Fatalf("got %dx%d", width, height)
	}
}
