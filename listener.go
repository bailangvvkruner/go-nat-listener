package nattraversal

import (
	"context"
	"fmt"

	"github.com/go-i2p/logger"
)

// Listen creates a TCP listener with NAT traversal on the specified port.
// This is a convenience wrapper around ListenContext using context.Background().
func Listen(port int) (*NATListener, error) {
	return ListenContext(context.Background(), port)
}

// ListenContext creates a TCP listener with NAT traversal on the specified port.
// The context can be used to cancel the discovery and mapping operations.
// Once the listener is created, the context is no longer used - use Close() to stop the listener.
func ListenContext(ctx context.Context, port int) (*NATListener, error) {
	log.WithField("port", port).Debug("creating NAT TCP listener")

	// Check context before starting
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("context cancelled before starting: %w", err)
	}

	mapper, externalPort, err := createTCPMappingContext(ctx, port)
	if err != nil {
		log.WithError(err).WithField("port", port).Error("failed to create TCP port mapping")
		return nil, fmt.Errorf("failed to create port mapping: %w", err)
	}

	log.WithFields(logger.Fields{
		"internalPort": port,
		"externalPort": externalPort,
	}).Debug("TCP port mapping created")

	// Check context after mapping
	if err := ctx.Err(); err != nil {
		mapper.UnmapPort("TCP", externalPort)
		return nil, fmt.Errorf("context cancelled after mapping: %w", err)
	}

	listener, err := listenTCPContext(ctx, port)
	if err != nil {
		mapper.UnmapPort("TCP", externalPort)
		log.WithError(err).WithField("port", port).Error("failed to bind TCP listener")
		return nil, fmt.Errorf("failed to create listener: %w", err)
	}

	// Check context after creating listener
	if err := ctx.Err(); err != nil {
		listener.Close()
		mapper.UnmapPort("TCP", externalPort)
		return nil, fmt.Errorf("context cancelled after listener creation: %w", err)
	}

	// Get addresses for NATAddr
	internalAddr := listener.Addr().String()
	externalIP, err := mapper.GetExternalIP()
	if err != nil {
		listener.Close()
		mapper.UnmapPort("TCP", externalPort)
		log.WithError(err).WithField("port", port).Error("failed to get external IP for TCP listener")
		return nil, fmt.Errorf("failed to get external IP: %w", err)
	}

	externalAddr := fmt.Sprintf("%s:%d", externalIP, externalPort)
	addr := NewNATAddr("tcp", internalAddr, externalAddr)

	renewal := NewRenewalManager(mapper, "TCP", port, externalPort)

	natListener := &NATListener{
		listener:     listener,
		renewal:      renewal,
		externalPort: externalPort,
		externalIP:   externalIP,
		addr:         addr,
	}

	// Set up callback to handle external port changes during renewal
	renewal.SetPortChangeCallback(natListener.updateExternalPort)
	renewal.Start()

	log.WithFields(logger.Fields{
		"internalAddr": internalAddr,
		"externalAddr": externalAddr,
	}).Debug("NAT TCP listener ready")
	return natListener, nil
}

// ListenWithFallback creates a TCP listener with NAT traversal on the specified port.
// If NAT traversal fails (UPnP and NAT-PMP both unavailable), it falls back to a
// standard net.Listener without NAT hole-punching.
// This is a convenience wrapper around ListenWithFallbackContext using context.Background().
func ListenWithFallback(port int) (*NATListener, error) {
	return ListenWithFallbackContext(context.Background(), port)
}

// ListenWithFallbackContext creates a TCP listener with NAT traversal on the specified port.
// If NAT traversal fails (UPnP and NAT-PMP both unavailable), it falls back to a
// standard net.Listener without NAT hole-punching.
// The context can be used to cancel the discovery and mapping operations.
// Once the listener is created, the context is no longer used - use Close() to stop the listener.
//
// When fallback is used:
//   - ExternalPort() returns the same as the internal port
//   - Addr() returns a NATAddr where internal and external addresses are the same
//   - No port renewal is performed (the renewal manager is nil)
//   - IsFallback() returns true
func ListenWithFallbackContext(ctx context.Context, port int) (*NATListener, error) {
	log.WithField("port", port).Debug("creating NAT TCP listener with fallback")

	// Check context before starting
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("context cancelled before starting: %w", err)
	}

	// Try NAT traversal first
	natListener, err := ListenContext(ctx, port)
	if err == nil {
		return natListener, nil
	}

	log.WithError(err).WithField("port", port).Warn("NAT traversal failed, falling back to standard TCP listener")

	// Check context before fallback
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("context cancelled after NAT attempt: %w", err)
	}

	// NAT traversal failed, fall back to standard listener
	listener, listenErr := listenTCPContext(ctx, port)
	if listenErr != nil {
		log.WithError(listenErr).WithField("port", port).Error("fallback TCP listener creation failed")
		return nil, fmt.Errorf("failed to create fallback listener: %w (NAT error: %v)", listenErr, err)
	}

	// For fallback, internal and external addresses are the same (local address)
	internalAddr := listener.Addr().String()
	addr := NewNATAddr("tcp", internalAddr, internalAddr)

	log.WithFields(logger.Fields{
		"port":         port,
		"internalAddr": internalAddr,
	}).Info("TCP listener started in fallback mode (no NAT traversal)")

	return &NATListener{
		listener:     listener,
		renewal:      nil, // No renewal for fallback
		externalPort: port,
		externalIP:   "", // Unknown external IP in fallback mode
		addr:         addr,
		fallback:     true,
	}, nil
}
