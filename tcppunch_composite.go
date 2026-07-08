package nattraversal

import (
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/go-i2p/logger"
)

// TCPPunchPortMapper composes an inner router mapping with a TCP punch mapping.
// The inner mapper opens the LAN router path, while the punch mapper reports
// and maintains the upstream public TCP endpoint.
type TCPPunchPortMapper struct {
	inner PortMapper
	punch PortMapper

	mu      sync.Mutex
	mapping *tcpPunchPortMapping
}

type tcpPunchPortMapping struct {
	internalPort      int
	innerExternalPort int
	punchExternalPort int
}

// Ensure TCPPunchPortMapper satisfies the PortMapper interface.
var _ PortMapper = (*TCPPunchPortMapper)(nil)

// NewTCPPunchPortMapper creates a mapper that first maps the inner router and
// then establishes a TCP punch binding from the same local port.
func NewTCPPunchPortMapper(inner PortMapper, punch PortMapper) (*TCPPunchPortMapper, error) {
	if inner == nil {
		return nil, fmt.Errorf("inner port mapper is required")
	}
	if punch == nil {
		return nil, fmt.Errorf("TCP punch mapper is required")
	}
	return &TCPPunchPortMapper{inner: inner, punch: punch}, nil
}

// MapPort opens or renews the inner TCP port mapping, then opens or refreshes
// the upstream TCP punch mapping. The returned port is the public punch port.
func (m *TCPPunchPortMapper) MapPort(protocol string, internalPort int, duration time.Duration) (int, error) {
	if strings.ToUpper(protocol) != "TCP" {
		return 0, fmt.Errorf("TCP punch composite only supports TCP, got %s", protocol)
	}
	if internalPort < 1 || internalPort > 65535 {
		return 0, fmt.Errorf("invalid port number: %d (must be 1-65535)", internalPort)
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	if m.mapping != nil && m.mapping.internalPort != internalPort {
		return 0, fmt.Errorf("TCP punch composite already has an active mapping for port %d", m.mapping.internalPort)
	}

	innerExternalPort, err := m.inner.MapPort("TCP", internalPort, duration)
	if err != nil {
		return 0, fmt.Errorf("inner TCP port mapping failed: %w", err)
	}

	punchExternalPort, err := m.punch.MapPort("TCP", internalPort, duration)
	if err != nil {
		m.cleanupFailedInnerMapping(innerExternalPort)
		return 0, fmt.Errorf("TCP punch mapping failed: %w", err)
	}

	oldInnerExternalPort := 0
	if m.mapping != nil {
		oldInnerExternalPort = m.mapping.innerExternalPort
	}
	m.mapping = &tcpPunchPortMapping{
		internalPort:      internalPort,
		innerExternalPort: innerExternalPort,
		punchExternalPort: punchExternalPort,
	}

	if oldInnerExternalPort != 0 && oldInnerExternalPort != innerExternalPort {
		if err := m.inner.UnmapPort("TCP", oldInnerExternalPort); err != nil {
			log.WithError(err).WithFields(logger.Fields{
				"protocol":     "TCP",
				"externalPort": oldInnerExternalPort,
			}).Warn("failed to unmap old inner TCP port after renewal")
		}
	}

	log.WithFields(logger.Fields{
		"internalPort":      internalPort,
		"innerExternalPort": innerExternalPort,
		"punchExternalPort": punchExternalPort,
	}).Debug("TCP punch composite mapping established")
	return punchExternalPort, nil
}

// UnmapPort stops the upstream TCP punch and then removes the inner router map.
func (m *TCPPunchPortMapper) UnmapPort(protocol string, externalPort int) error {
	if strings.ToUpper(protocol) != "TCP" {
		return fmt.Errorf("TCP punch composite only supports TCP, got %s", protocol)
	}

	m.mu.Lock()
	state := m.mapping
	if state == nil || (externalPort != 0 && state.punchExternalPort != externalPort) {
		m.mu.Unlock()
		return nil
	}
	m.mapping = nil
	m.mu.Unlock()

	var joined error
	if err := m.punch.UnmapPort("TCP", externalPort); err != nil {
		joined = errors.Join(joined, fmt.Errorf("TCP punch unmap failed: %w", err))
	}
	if state != nil {
		if err := m.inner.UnmapPort("TCP", state.innerExternalPort); err != nil {
			joined = errors.Join(joined, fmt.Errorf("inner TCP port unmap failed: %w", err))
		}
	}
	return joined
}

// GetExternalIP returns the public IP reported by the TCP punch mapping.
func (m *TCPPunchPortMapper) GetExternalIP() (string, error) {
	return m.punch.GetExternalIP()
}

func (m *TCPPunchPortMapper) cleanupFailedInnerMapping(innerExternalPort int) {
	if m.mapping != nil && m.mapping.innerExternalPort == innerExternalPort {
		return
	}
	if err := m.inner.UnmapPort("TCP", innerExternalPort); err != nil {
		log.WithError(err).WithFields(logger.Fields{
			"protocol":     "TCP",
			"externalPort": innerExternalPort,
		}).Warn("failed to clean up inner TCP port after punch failure")
	}
}
