//go:build windows

package observation

import (
	"context"
	"fmt"
	"net"
)

func listenObserverSocket(string) (net.Listener, error) {
	return nil, fmt.Errorf("observer transport is unsupported on windows")
}

func dialObserverSocket(context.Context, string) (net.Conn, error) {
	return nil, fmt.Errorf("observer transport is unsupported on windows")
}

func removeObserverSocket(string) error { return nil }
