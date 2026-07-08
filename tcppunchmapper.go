package nattraversal

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/go-i2p/logger"
)

const (
	envTCPPunchEnable            = "NATLISTENER_TCP_PUNCH_ENABLE"
	envTCPPunchSTUNAddr          = "NATLISTENER_TCP_PUNCH_STUN_ADDR"
	envTCPPunchKeepAliveAddr     = "NATLISTENER_TCP_PUNCH_KEEPALIVE_ADDR"
	envTCPPunchLocalIP           = "NATLISTENER_TCP_PUNCH_LOCAL_IP"
	envTCPPunchDialTimeout       = "NATLISTENER_TCP_PUNCH_DIAL_TIMEOUT"
	envTCPPunchKeepAliveInterval = "NATLISTENER_TCP_PUNCH_KEEPALIVE_INTERVAL"
	envTCPPunchRetryInterval     = "NATLISTENER_TCP_PUNCH_RETRY_INTERVAL"
	envTCPPunchKeepAlivePayload  = "NATLISTENER_TCP_PUNCH_KEEPALIVE_PAYLOAD"
)

var errTCPPunchDisabled = errors.New("TCP punch mapper disabled")

// TCPPunchConfig configures TCP hole punching through a long-lived outbound
// connection and a TCP STUN probe from the same local port.
type TCPPunchConfig struct {
	STUNAddress       string
	KeepAliveAddress  string
	LocalIP           string
	DialTimeout       time.Duration
	KeepAliveInterval time.Duration
	RetryInterval     time.Duration
	KeepAlivePayload  []byte
}

// TCPPunchMapper implements PortMapper by keeping a TCP NAT binding alive from
// the listening port and reporting the public endpoint observed through TCP STUN.
type TCPPunchMapper struct {
	config TCPPunchConfig

	mu      sync.Mutex
	mapping *tcpPunchMapping
}

type tcpPunchMapping struct {
	internalPort int
	externalIP   string
	externalPort int
	cancel       context.CancelFunc
	done         chan struct{}

	connMu sync.Mutex
	conn   net.Conn
}

// Ensure TCPPunchMapper satisfies the PortMapper interface.
var _ PortMapper = (*TCPPunchMapper)(nil)

// NewTCPPunchMapper creates a mapper from explicit configuration.
func NewTCPPunchMapper(config TCPPunchConfig) (*TCPPunchMapper, error) {
	config = defaultTCPPunchConfig(config)
	if config.STUNAddress == "" {
		return nil, fmt.Errorf("TCP punch STUN address is required")
	}
	if config.KeepAliveAddress == "" {
		return nil, fmt.Errorf("TCP punch keepalive address is required")
	}
	if _, _, err := net.SplitHostPort(config.STUNAddress); err != nil {
		return nil, fmt.Errorf("invalid TCP punch STUN address %q: %w", config.STUNAddress, err)
	}
	if _, _, err := net.SplitHostPort(config.KeepAliveAddress); err != nil {
		return nil, fmt.Errorf("invalid TCP punch keepalive address %q: %w", config.KeepAliveAddress, err)
	}

	return &TCPPunchMapper{config: config}, nil
}

func newTCPPunchMapperFromEnv() (*TCPPunchMapper, error) {
	if !truthyEnv(os.Getenv(envTCPPunchEnable)) {
		return nil, errTCPPunchDisabled
	}

	config := TCPPunchConfig{
		STUNAddress:      strings.TrimSpace(os.Getenv(envTCPPunchSTUNAddr)),
		KeepAliveAddress: strings.TrimSpace(os.Getenv(envTCPPunchKeepAliveAddr)),
		LocalIP:          strings.TrimSpace(os.Getenv(envTCPPunchLocalIP)),
	}

	if value := strings.TrimSpace(os.Getenv(envTCPPunchDialTimeout)); value != "" {
		duration, err := time.ParseDuration(value)
		if err != nil {
			return nil, fmt.Errorf("invalid %s: %w", envTCPPunchDialTimeout, err)
		}
		config.DialTimeout = duration
	}
	if value := strings.TrimSpace(os.Getenv(envTCPPunchKeepAliveInterval)); value != "" {
		duration, err := time.ParseDuration(value)
		if err != nil {
			return nil, fmt.Errorf("invalid %s: %w", envTCPPunchKeepAliveInterval, err)
		}
		config.KeepAliveInterval = duration
	}
	if value := strings.TrimSpace(os.Getenv(envTCPPunchRetryInterval)); value != "" {
		duration, err := time.ParseDuration(value)
		if err != nil {
			return nil, fmt.Errorf("invalid %s: %w", envTCPPunchRetryInterval, err)
		}
		config.RetryInterval = duration
	}
	if value := os.Getenv(envTCPPunchKeepAlivePayload); value != "" {
		config.KeepAlivePayload = []byte(unescapePayload(value))
	}

	return NewTCPPunchMapper(config)
}

func defaultTCPPunchConfig(config TCPPunchConfig) TCPPunchConfig {
	if config.DialTimeout <= 0 {
		config.DialTimeout = 5 * time.Second
	}
	if config.KeepAliveInterval <= 0 {
		config.KeepAliveInterval = 30 * time.Second
	}
	if config.RetryInterval <= 0 {
		config.RetryInterval = 2 * time.Second
	}
	if config.KeepAlivePayload == nil {
		config.KeepAlivePayload = defaultKeepAlivePayload(config.KeepAliveAddress)
	}
	return config
}

func truthyEnv(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "1", "true", "yes", "y", "on", "enabled":
		return true
	default:
		return false
	}
}

func unescapePayload(value string) string {
	replacer := strings.NewReplacer(`\r`, "\r", `\n`, "\n", `\t`, "\t")
	return replacer.Replace(value)
}

func defaultKeepAlivePayload(address string) []byte {
	host, port, err := net.SplitHostPort(address)
	if err != nil || port != "80" {
		return nil
	}
	return []byte(fmt.Sprintf("HEAD / HTTP/1.1\r\nHost: %s\r\nConnection: keep-alive\r\n\r\n", host))
}

// MapPort creates and maintains a TCP outbound NAT binding from internalPort.
func (m *TCPPunchMapper) MapPort(protocol string, internalPort int, _ time.Duration) (int, error) {
	if strings.ToUpper(protocol) != "TCP" {
		return 0, fmt.Errorf("TCP punch only supports TCP, got %s", protocol)
	}
	if internalPort < 1 || internalPort > 65535 {
		return 0, fmt.Errorf("invalid port number: %d (must be 1-65535)", internalPort)
	}

	m.mu.Lock()
	current := m.mapping
	m.mu.Unlock()

	if current != nil && current.internalPort == internalPort {
		if ip, port, err := m.queryMappedAddress(internalPort); err == nil {
			m.mu.Lock()
			current.externalIP = ip
			current.externalPort = port
			m.mu.Unlock()
		} else {
			log.WithError(err).Warn("TCP punch STUN refresh failed, keeping last public endpoint")
		}

		m.mu.Lock()
		externalPort := current.externalPort
		m.mu.Unlock()
		return externalPort, nil
	}
	if current != nil {
		return 0, fmt.Errorf("TCP punch mapper already has an active mapping for port %d", current.internalPort)
	}

	localIP, err := m.localIP()
	if err != nil {
		return 0, err
	}

	log.WithFields(logger.Fields{
		"internalPort":    internalPort,
		"localIP":         localIP,
		"keepAliveServer": m.config.KeepAliveAddress,
		"stunServer":      m.config.STUNAddress,
	}).Debug("creating TCP punch mapping")

	conn, err := m.dialFromPort(localIP, internalPort, m.config.KeepAliveAddress)
	if err != nil {
		return 0, fmt.Errorf("TCP punch keepalive dial failed: %w", err)
	}

	externalIP, externalPort, err := m.queryMappedAddressWithLocalIP(localIP, internalPort)
	if err != nil {
		conn.Close()
		return 0, fmt.Errorf("TCP punch STUN lookup failed: %w", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	state := &tcpPunchMapping{
		internalPort: internalPort,
		externalIP:   externalIP,
		externalPort: externalPort,
		cancel:       cancel,
		done:         make(chan struct{}),
	}
	state.setConn(conn)

	m.mu.Lock()
	if m.mapping != nil {
		m.mu.Unlock()
		cancel()
		state.closeConn()
		return 0, fmt.Errorf("TCP punch mapper already has an active mapping")
	}
	m.mapping = state
	m.mu.Unlock()

	go m.keepAliveLoop(ctx, state, localIP, internalPort, conn)

	log.WithFields(logger.Fields{
		"internalPort": internalPort,
		"externalIP":   externalIP,
		"externalPort": externalPort,
	}).Debug("TCP punch mapping created")
	return externalPort, nil
}

// UnmapPort stops the keepalive connection. The remote NAT entry expires
// naturally after the TCP binding is closed.
func (m *TCPPunchMapper) UnmapPort(protocol string, _ int) error {
	if strings.ToUpper(protocol) != "TCP" {
		return fmt.Errorf("TCP punch only supports TCP, got %s", protocol)
	}

	m.mu.Lock()
	state := m.mapping
	m.mapping = nil
	m.mu.Unlock()

	if state == nil {
		return nil
	}

	state.cancel()
	state.closeConn()
	<-state.done
	log.WithField("internalPort", state.internalPort).Debug("TCP punch mapping stopped")
	return nil
}

// GetExternalIP returns the public IPv4 address from the active TCP punch map.
func (m *TCPPunchMapper) GetExternalIP() (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.mapping == nil || m.mapping.externalIP == "" {
		return "", fmt.Errorf("TCP punch mapping is not active")
	}
	return m.mapping.externalIP, nil
}

func (m *TCPPunchMapper) localIP() (string, error) {
	if m.config.LocalIP != "" {
		ip := net.ParseIP(m.config.LocalIP)
		if ip == nil || ip.To4() == nil {
			return "", fmt.Errorf("invalid TCP punch local IPv4 address: %s", m.config.LocalIP)
		}
		return ip.String(), nil
	}
	return discoverLocalIPv4ForRemote(m.config.KeepAliveAddress, m.config.DialTimeout)
}

func discoverLocalIPv4ForRemote(remoteAddress string, timeout time.Duration) (string, error) {
	dialer := net.Dialer{Timeout: timeout}
	conn, err := dialer.Dial("udp4", remoteAddress)
	if err != nil {
		return "", fmt.Errorf("failed to discover local IPv4 for %s: %w", remoteAddress, err)
	}
	defer conn.Close()

	addr, ok := conn.LocalAddr().(*net.UDPAddr)
	if !ok || addr.IP == nil || addr.IP.To4() == nil {
		return "", fmt.Errorf("unexpected local address for %s: %v", remoteAddress, conn.LocalAddr())
	}
	return addr.IP.String(), nil
}

func (m *TCPPunchMapper) dialFromPort(localIP string, localPort int, remoteAddress string) (net.Conn, error) {
	ctx, cancel := context.WithTimeout(context.Background(), m.config.DialTimeout)
	defer cancel()

	dialer := net.Dialer{
		Timeout:   m.config.DialTimeout,
		LocalAddr: &net.TCPAddr{IP: net.ParseIP(localIP), Port: localPort},
		Control:   reuseAddrControl,
	}
	return dialer.DialContext(ctx, "tcp4", remoteAddress)
}

func reuseAddrControl(_ string, _ string, conn syscall.RawConn) error {
	var controlErr error
	if err := conn.Control(func(fd uintptr) {
		controlErr = setSocketReuseAddr(fd)
	}); err != nil {
		return err
	}
	return controlErr
}

func (m *TCPPunchMapper) queryMappedAddress(internalPort int) (string, int, error) {
	localIP, err := m.localIP()
	if err != nil {
		return "", 0, err
	}
	return m.queryMappedAddressWithLocalIP(localIP, internalPort)
}

func (m *TCPPunchMapper) queryMappedAddressWithLocalIP(localIP string, internalPort int) (string, int, error) {
	conn, err := m.dialFromPort(localIP, internalPort, m.config.STUNAddress)
	if err != nil {
		return "", 0, err
	}
	defer conn.Close()

	if err := conn.SetDeadline(time.Now().Add(m.config.DialTimeout)); err != nil {
		return "", 0, err
	}

	request, txID, err := newSTUNBindingRequest()
	if err != nil {
		return "", 0, err
	}
	if _, err := conn.Write(request); err != nil {
		return "", 0, err
	}

	header := make([]byte, 20)
	if _, err := io.ReadFull(conn, header); err != nil {
		return "", 0, err
	}
	length := int(binary.BigEndian.Uint16(header[2:4]))
	if length < 0 || length > 2048 {
		return "", 0, fmt.Errorf("invalid STUN message length: %d", length)
	}

	body := make([]byte, length)
	if _, err := io.ReadFull(conn, body); err != nil {
		return "", 0, err
	}

	response := append(header, body...)
	ip, port, err := parseSTUNMappedAddress(response, txID)
	if err != nil {
		return "", 0, err
	}
	return ip, port, nil
}

func newSTUNBindingRequest() ([]byte, []byte, error) {
	txID := make([]byte, 12)
	if _, err := rand.Read(txID); err != nil {
		return nil, nil, err
	}

	request := make([]byte, 20)
	binary.BigEndian.PutUint16(request[0:2], 0x0001)
	binary.BigEndian.PutUint16(request[2:4], 0)
	binary.BigEndian.PutUint32(request[4:8], 0x2112A442)
	copy(request[8:20], txID)
	return request, txID, nil
}

func parseSTUNMappedAddress(message []byte, txID []byte) (string, int, error) {
	if len(message) < 20 {
		return "", 0, fmt.Errorf("short STUN response")
	}
	if binary.BigEndian.Uint16(message[0:2]) != 0x0101 {
		return "", 0, fmt.Errorf("unexpected STUN response type 0x%04x", binary.BigEndian.Uint16(message[0:2]))
	}
	if binary.BigEndian.Uint32(message[4:8]) != 0x2112A442 {
		return "", 0, fmt.Errorf("unexpected STUN magic cookie")
	}
	if !equalBytes(message[8:20], txID) {
		return "", 0, fmt.Errorf("STUN transaction ID mismatch")
	}

	length := int(binary.BigEndian.Uint16(message[2:4]))
	if len(message) < 20+length {
		return "", 0, fmt.Errorf("truncated STUN attributes")
	}

	for offset := 20; offset+4 <= 20+length; {
		attrType := binary.BigEndian.Uint16(message[offset : offset+2])
		attrLen := int(binary.BigEndian.Uint16(message[offset+2 : offset+4]))
		valueStart := offset + 4
		valueEnd := valueStart + attrLen
		if valueEnd > len(message) {
			return "", 0, fmt.Errorf("truncated STUN attribute")
		}

		if attrType == 0x0020 || attrType == 0x0001 {
			ip, port, err := parseSTUNAddressAttribute(attrType, message[valueStart:valueEnd])
			if err == nil {
				return ip, port, nil
			}
		}

		offset = valueEnd
		if rem := offset % 4; rem != 0 {
			offset += 4 - rem
		}
	}

	return "", 0, fmt.Errorf("STUN response did not include an IPv4 mapped address")
}

func parseSTUNAddressAttribute(attrType uint16, value []byte) (string, int, error) {
	if len(value) < 8 {
		return "", 0, fmt.Errorf("short STUN address attribute")
	}
	if value[1] != 0x01 {
		return "", 0, fmt.Errorf("STUN mapped address is not IPv4")
	}

	port := int(binary.BigEndian.Uint16(value[2:4]))
	ip := []byte{value[4], value[5], value[6], value[7]}
	if attrType == 0x0020 {
		port ^= 0x2112
		cookie := []byte{0x21, 0x12, 0xA4, 0x42}
		for i := range ip {
			ip[i] ^= cookie[i]
		}
	}

	return net.IPv4(ip[0], ip[1], ip[2], ip[3]).String(), port, nil
}

func equalBytes(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	var diff byte
	for i := range a {
		diff |= a[i] ^ b[i]
	}
	return diff == 0
}

func (m *TCPPunchMapper) keepAliveLoop(ctx context.Context, state *tcpPunchMapping, localIP string, internalPort int, initial net.Conn) {
	defer close(state.done)

	conn := initial
	for {
		if conn != nil {
			state.setConn(conn)
			err := m.runKeepAliveConn(ctx, conn)
			state.clearConn(conn)
			conn.Close()
			if ctx.Err() != nil {
				return
			}
			log.WithError(err).Warn("TCP punch keepalive connection ended, reconnecting")
		}

		select {
		case <-ctx.Done():
			return
		case <-time.After(m.config.RetryInterval):
		}

		next, err := m.dialFromPort(localIP, internalPort, m.config.KeepAliveAddress)
		if err != nil {
			log.WithError(err).Warn("TCP punch keepalive reconnect failed")
			continue
		}
		conn = next
	}
}

func (m *TCPPunchMapper) runKeepAliveConn(ctx context.Context, conn net.Conn) error {
	if tcpConn, ok := conn.(*net.TCPConn); ok {
		_ = tcpConn.SetKeepAlive(true)
		_ = tcpConn.SetKeepAlivePeriod(m.config.KeepAliveInterval)
	}

	buffer := make([]byte, 1024)
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		if len(m.config.KeepAlivePayload) > 0 {
			if err := conn.SetWriteDeadline(time.Now().Add(m.config.DialTimeout)); err != nil {
				return err
			}
			if _, err := conn.Write(m.config.KeepAlivePayload); err != nil {
				return err
			}
		}

		_ = conn.SetReadDeadline(time.Now().Add(200 * time.Millisecond))
		for {
			_, err := conn.Read(buffer)
			if err == nil {
				continue
			}
			if errors.Is(err, io.EOF) {
				return err
			}
			var netErr net.Error
			if errors.As(err, &netErr) && netErr.Timeout() {
				break
			}
			return err
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(m.config.KeepAliveInterval):
		}
	}
}

func (s *tcpPunchMapping) setConn(conn net.Conn) {
	s.connMu.Lock()
	defer s.connMu.Unlock()
	s.conn = conn
}

func (s *tcpPunchMapping) clearConn(conn net.Conn) {
	s.connMu.Lock()
	defer s.connMu.Unlock()
	if s.conn == conn {
		s.conn = nil
	}
}

func (s *tcpPunchMapping) closeConn() {
	s.connMu.Lock()
	defer s.connMu.Unlock()
	if s.conn != nil {
		s.conn.Close()
		s.conn = nil
	}
}
