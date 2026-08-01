package terminal

import (
	"context"
	"fmt"
	"os/exec"
	"runtime"
	"sort"
	"strconv"
	"strings"
)

type Port struct {
	Protocol string `json:"protocol"`
	Address  string `json:"address"`
	Port     int    `json:"port"`
}

// ListeningPorts returns TCP listeners owned by a task process.
func ListeningPorts(ctx context.Context, pid int) ([]Port, error) {
	if pid <= 0 {
		return nil, fmt.Errorf("task has no process id")
	}
	if runtime.GOOS == "windows" {
		output, err := exec.CommandContext(ctx, "netstat", "-ano", "-p", "tcp").Output()
		if err != nil {
			return nil, fmt.Errorf("run netstat: %w", err)
		}
		return parseNetstat(string(output), pid), nil
	}
	output, err := exec.CommandContext(ctx, "lsof", "-nP", "-a", "-p", strconv.Itoa(pid), "-iTCP", "-sTCP:LISTEN", "-Fn").Output()
	if err != nil {
		if _, lookupErr := exec.LookPath("lsof"); lookupErr != nil {
			return nil, fmt.Errorf("lsof is required for port inspection")
		}
		// lsof uses exit status 1 when no matching listener exists.
		if len(output) == 0 {
			return []Port{}, nil
		}
	}
	return parseLsof(string(output)), nil
}

func parseLsof(output string) []Port {
	seen := map[string]bool{}
	var ports []Port
	for _, line := range strings.Split(output, "\n") {
		if !strings.HasPrefix(line, "n") {
			continue
		}
		address := strings.TrimSpace(strings.TrimPrefix(line, "n"))
		if strings.HasPrefix(address, "TCP ") {
			address = strings.TrimSpace(strings.TrimPrefix(address, "TCP "))
		}
		if arrow := strings.Index(address, "->"); arrow >= 0 {
			continue
		}
		port, ok := endpointPort(address)
		if !ok {
			continue
		}
		key := fmt.Sprintf("tcp|%s|%d", address, port)
		if !seen[key] {
			seen[key] = true
			ports = append(ports, Port{Protocol: "tcp", Address: address, Port: port})
		}
	}
	sort.Slice(ports, func(i, j int) bool {
		if ports[i].Port == ports[j].Port {
			return ports[i].Address < ports[j].Address
		}
		return ports[i].Port < ports[j].Port
	})
	return ports
}

func parseNetstat(output string, pid int) []Port {
	var ports []Port
	seen := map[string]bool{}
	for _, line := range strings.Split(output, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 5 || !strings.EqualFold(fields[0], "TCP") || !strings.EqualFold(fields[3], "LISTENING") || fields[4] != strconv.Itoa(pid) {
			continue
		}
		port, ok := endpointPort(fields[1])
		if !ok || seen[fields[1]] {
			continue
		}
		seen[fields[1]] = true
		ports = append(ports, Port{Protocol: "tcp", Address: fields[1], Port: port})
	}
	return ports
}

func endpointPort(endpoint string) (int, bool) {
	colon := strings.LastIndex(endpoint, ":")
	if colon < 0 || colon == len(endpoint)-1 {
		return 0, false
	}
	port, err := strconv.Atoi(strings.TrimSuffix(endpoint[colon+1:], " (LISTEN)"))
	return port, err == nil && port > 0 && port <= 65535
}
