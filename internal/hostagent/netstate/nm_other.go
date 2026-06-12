//go:build !linux

// Non-Linux stub for NMProvider. NetworkManager and the system DBus are
// Linux-only; cmd/host-agent-real is the only consumer. This exists so
// go build ./... compiles in the dev-on-Mac workflow, mirroring
// avahipublisher's dbus_other.go.
package netstate

import (
	"context"
	"errors"
	"time"
)

var errUnsupported = errors.New("netstate: NetworkManager provider not supported on this OS")

// NMProvider is a non-functional stub on non-Linux platforms.
type NMProvider struct {
	Debounce time.Duration
}

func (p *NMProvider) LANInterfaces() ([]LANInterface, error) { return nil, errUnsupported }

func (p *NMProvider) Watch(ctx context.Context, onChange func()) error { return errUnsupported }

func (p *NMProvider) Close() error { return nil }
