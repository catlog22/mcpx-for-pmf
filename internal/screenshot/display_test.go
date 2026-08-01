package screenshot

import "testing"

func TestParseDarwinDisplays(t *testing.T) {
	payload := []byte(`{"SPDisplaysDataType":[{"spdisplays_ndrvs":[{"_name":"Built-in Display","_spdisplays_resolution":"1728 x 1117 Retina","spdisplays_main":"spdisplays_yes"}]}]}`)
	displays := parseDarwinDisplays(payload)
	if len(displays) != 1 || displays[0].Width != 1728 || displays[0].Height != 1117 || displays[0].Scale != 2 || !displays[0].Primary {
		t.Fatalf("displays: %+v", displays)
	}
}

func TestParseXrandrDisplays(t *testing.T) {
	output := "eDP-1 connected primary 1920x1080+0+0 normal\nHDMI-1 connected 2560x1440+1920+-120 normal\n"
	displays := parseXrandrDisplays(output)
	if len(displays) != 2 || !displays[0].Primary || displays[1].X != 1920 || displays[1].Y != -120 {
		t.Fatalf("displays: %+v", displays)
	}
}

func TestParseWindowsDisplays(t *testing.T) {
	payload := []byte(`[{"name":"DISPLAY1","x":0,"y":0,"width":1920,"height":1080,"primary":true}]`)
	displays := parseWindowsDisplays(payload)
	if len(displays) != 1 || displays[0].Width != 1920 || !displays[0].Primary {
		t.Fatalf("displays: %+v", displays)
	}
}
