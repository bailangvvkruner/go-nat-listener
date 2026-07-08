// Package nattraversal provides NAT traversal using TCP punch, UPnP, and
// NAT-PMP protocols with standard Go network interfaces and automatic renewal.
package nattraversal

import (
	"context"
	"errors"
	"fmt"
)

// NewPortMapper creates a port mapper, trying direct connectivity first,
// then UPnP, then NAT-PMP.
// This is a convenience wrapper around NewPortMapperContext using context.Background().
func NewPortMapper() (PortMapper, error) {
	return NewPortMapperContext(context.Background())
}

// NewPortMapperContext creates a port mapper with context support, trying direct
// connectivity first, then UPnP, then NAT-PMP.
// The context is passed through to the discovery process, allowing cancellation during slow network operations.
func NewPortMapperContext(ctx context.Context) (PortMapper, error) {
	log.Debug("discovering port mapper")

	// Check context before starting
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("context cancelled: %w", err)
	}

	// Try direct connectivity first.
	direct, err := newDirectPortMapper()
	if err == nil {
		log.Debug("direct port mapper selected")
		return direct, nil
	}

	log.WithError(err).Debug("direct connectivity detection failed, trying UPnP")

	// Try UPnP with context support
	upnp, err := NewUPnPMapperContext(ctx)
	if err == nil {
		log.Debug("UPnP port mapper selected")
		return upnp, nil
	}

	log.WithError(err).Debug("UPnP discovery failed, trying NAT-PMP")

	// Check context before fallback
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("context cancelled after UPnP attempt: %w", err)
	}

	// Fall back to NAT-PMP
	natpmp, err := NewNATPMPMapper()
	if err != nil {
		log.WithError(err).Error("all NAT traversal protocols failed")
		return nil, fmt.Errorf("no NAT traversal available: UPnP failed, NAT-PMP failed: %w", err)
	}

	log.Debug("NAT-PMP port mapper selected")
	return natpmp, nil
}

// NewTCPPortMapperContext creates a TCP-capable port mapper. When explicitly
// enabled through environment configuration, it maps the inner router first and
// then creates a TCP punch binding that can expose an upstream CGNAT-assigned
// public port.
func NewTCPPortMapperContext(ctx context.Context) (PortMapper, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("context cancelled: %w", err)
	}

	tcpPunch, err := newTCPPunchMapperFromEnv()
	if err == nil {
		inner, innerErr := newInnerPortMapperContext(ctx)
		if innerErr == nil {
			composite, compositeErr := NewTCPPunchPortMapper(inner, tcpPunch)
			if compositeErr != nil {
				return nil, compositeErr
			}
			log.Debug("TCP punch composite port mapper selected")
			return composite, nil
		}

		log.WithError(innerErr).Warn("inner TCP port mapper unavailable, using TCP punch mapper without inner router mapping")
		log.Debug("TCP punch port mapper selected")
		return tcpPunch, nil
	}
	if !errors.Is(err, errTCPPunchDisabled) {
		log.WithError(err).Warn("TCP punch mapper configuration failed, trying standard port mapper chain")
	} else {
		log.WithError(err).Debug("TCP punch mapper not enabled, trying standard port mapper chain")
	}

	return NewPortMapperContext(ctx)
}

func newInnerPortMapperContext(ctx context.Context) (PortMapper, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("context cancelled: %w", err)
	}

	upnp, upnpErr := NewUPnPMapperContext(ctx)
	if upnpErr == nil {
		log.Debug("UPnP inner TCP port mapper selected")
		return upnp, nil
	}

	log.WithError(upnpErr).Debug("UPnP inner mapper discovery failed, trying NAT-PMP")

	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("context cancelled after UPnP attempt: %w", err)
	}

	natpmp, natpmpErr := NewNATPMPMapper()
	if natpmpErr == nil {
		log.Debug("NAT-PMP inner TCP port mapper selected")
		return natpmp, nil
	}

	return nil, fmt.Errorf("no inner TCP port mapper available: UPnP failed: %v; NAT-PMP failed: %w", upnpErr, natpmpErr)
}
