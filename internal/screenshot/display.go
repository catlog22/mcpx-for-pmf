package screenshot

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

type Display struct {
	Index         int     `json:"index"`
	ID            string  `json:"id,omitempty"`
	Name          string  `json:"name,omitempty"`
	X             int     `json:"x"`
	Y             int     `json:"y"`
	Width         int     `json:"width"`
	Height        int     `json:"height"`
	Scale         float64 `json:"scale,omitempty"`
	Primary       bool    `json:"primary"`
	PositionKnown bool    `json:"position_known"`
}

var displayCache struct {
	sync.Mutex
	at       time.Time
	displays []Display
}

func Displays(ctx context.Context) []Display {
	displayCache.Lock()
	defer displayCache.Unlock()
	if time.Since(displayCache.at) < 30*time.Second && displayCache.displays != nil {
		return append([]Display(nil), displayCache.displays...)
	}
	var displays []Display
	switch runtime.GOOS {
	case "darwin":
		displays = displaysDarwin(ctx)
	case "linux":
		displays = displaysLinux(ctx)
	case "windows":
		displays = displaysWindows(ctx)
	}
	for index := range displays {
		displays[index].Index = index
	}
	if displays == nil {
		displays = []Display{}
	}
	displayCache.at, displayCache.displays = time.Now(), displays
	return append([]Display(nil), displays...)
}

func displaysDarwin(parent context.Context) []Display {
	output := displayCommand(parent, 5*time.Second, "system_profiler", "SPDisplaysDataType", "-json")
	return parseDarwinDisplays(output)
}

func parseDarwinDisplays(output []byte) []Display {
	var payload struct {
		Displays []struct {
			Drivers []map[string]any `json:"spdisplays_ndrvs"`
		} `json:"SPDisplaysDataType"`
	}
	if json.Unmarshal(output, &payload) != nil {
		return nil
	}
	resolution := regexp.MustCompile(`(?i)(\d+)\s*x\s*(\d+)`)
	var result []Display
	for _, adapter := range payload.Displays {
		for _, driver := range adapter.Drivers {
			value, _ := driver["_spdisplays_resolution"].(string)
			if value == "" {
				value, _ = driver["spdisplays_resolution"].(string)
			}
			if value == "" {
				value, _ = driver["_spdisplays_pixelresolution"].(string)
			}
			if value == "" {
				value, _ = driver["spdisplays_pixelresolution"].(string)
			}
			match := resolution.FindStringSubmatch(value)
			if len(match) != 3 {
				continue
			}
			width, _ := strconv.Atoi(match[1])
			height, _ := strconv.Atoi(match[2])
			name, _ := driver["_name"].(string)
			primary, _ := driver["spdisplays_main"].(string)
			scale := 1.0
			if strings.Contains(strings.ToLower(value), "retina") {
				scale = 2
			}
			result = append(result, Display{Name: name, Width: width, Height: height, Scale: scale, Primary: primary == "spdisplays_yes"})
		}
	}
	if len(result) > 0 && !hasPrimary(result) {
		result[0].Primary = true
	}
	return result
}

func displaysLinux(parent context.Context) []Display {
	output := string(displayCommand(parent, 2*time.Second, "xrandr", "--current"))
	result := parseXrandrDisplays(output)
	if len(result) == 0 {
		result = displaysLinuxDRM()
	}
	return result
}

func parseXrandrDisplays(output string) []Display {
	pattern := regexp.MustCompile(`(?m)^([^ ]+) connected( primary)? (\d+)x(\d+)\+(-?\d+)\+(-?\d+)`)
	var result []Display
	for _, match := range pattern.FindAllStringSubmatch(output, -1) {
		width, _ := strconv.Atoi(match[3])
		height, _ := strconv.Atoi(match[4])
		x, _ := strconv.Atoi(match[5])
		y, _ := strconv.Atoi(match[6])
		result = append(result, Display{Name: match[1], X: x, Y: y, Width: width, Height: height, Scale: 1, Primary: strings.Contains(match[0], " primary "), PositionKnown: true})
	}
	if len(result) > 0 && !hasPrimary(result) {
		result[0].Primary = true
	}
	sort.SliceStable(result, func(i, j int) bool { return result[i].Primary && !result[j].Primary })
	return result
}

func displaysLinuxDRM() []Display {
	matches, _ := filepath.Glob("/sys/class/drm/*/modes")
	pattern := regexp.MustCompile(`^(\d+)x(\d+)$`)
	var result []Display
	for _, path := range matches {
		content, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		match := pattern.FindStringSubmatch(strings.TrimSpace(strings.SplitN(string(content), "\n", 2)[0]))
		if len(match) != 3 {
			continue
		}
		width, _ := strconv.Atoi(match[1])
		height, _ := strconv.Atoi(match[2])
		result = append(result, Display{Name: filepath.Base(filepath.Dir(path)), Width: width, Height: height, Scale: 1})
	}
	return result
}

func displaysWindows(parent context.Context) []Display {
	script := `Add-Type -AssemblyName System.Windows.Forms; @([System.Windows.Forms.Screen]::AllScreens | ForEach-Object { [PSCustomObject]@{ name=$_.DeviceName; x=$_.Bounds.X; y=$_.Bounds.Y; width=$_.Bounds.Width; height=$_.Bounds.Height; primary=$_.Primary } }) | ConvertTo-Json -Compress`
	output := displayCommand(parent, 3*time.Second, "powershell", "-NoProfile", "-NonInteractive", "-Command", script)
	return parseWindowsDisplays(output)
}

func parseWindowsDisplays(output []byte) []Display {
	var raw []struct {
		Name    string `json:"name"`
		X       int    `json:"x"`
		Y       int    `json:"y"`
		Width   int    `json:"width"`
		Height  int    `json:"height"`
		Primary bool   `json:"primary"`
	}
	if len(output) == 0 {
		return nil
	}
	if output[0] == '{' {
		output = append(append([]byte{'['}, output...), ']')
	}
	if json.Unmarshal(output, &raw) != nil {
		return nil
	}
	result := make([]Display, 0, len(raw))
	for _, value := range raw {
		result = append(result, Display{Name: value.Name, X: value.X, Y: value.Y, Width: value.Width, Height: value.Height, Scale: 1, Primary: value.Primary, PositionKnown: true})
	}
	return result
}

func displayCommand(parent context.Context, timeout time.Duration, name string, args ...string) []byte {
	if _, err := exec.LookPath(name); err != nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()
	output, _ := exec.CommandContext(ctx, name, args...).Output()
	return output
}

func hasPrimary(displays []Display) bool {
	for _, display := range displays {
		if display.Primary {
			return true
		}
	}
	return false
}
