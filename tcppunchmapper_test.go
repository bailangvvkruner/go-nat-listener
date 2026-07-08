package nattraversal

import (
	"context"
	"encoding/binary"
	"io"
	"net"
	"testing"
	"time"
)

func TestParseSTUNMappedAddress(t *testing.T) {
	_, txID, err := newSTUNBindingRequest()
	if err != nil {
		t.Fatalf("failed to create STUN request: %v", err)
	}

	response := buildSTUNResponse(t, txID, "203.0.113.55", 45678)
	ip, port, err := parseSTUNMappedAddress(response, txID)
	if err != nil {
		t.Fatalf("parseSTUNMappedAddress failed: %v", err)
	}

	if ip != "203.0.113.55" {
		t.Fatalf("expected mapped IP 203.0.113.55, got %s", ip)
	}
	if port != 45678 {
		t.Fatalf("expected mapped port 45678, got %d", port)
	}
}

func TestTCPPunchMapperMapPort(t *testing.T) {
	keepAliveAddr, stopKeepAlive := startKeepAliveServer(t)
	defer stopKeepAlive()

	stunAddr, stopSTUN := startSTUNServer(t, "203.0.113.77", 45678)
	defer stopSTUN()

	internalPort := reserveTCPPort(t)
	mapper, err := NewTCPPunchMapper(TCPPunchConfig{
		STUNAddress:       stunAddr,
		KeepAliveAddress:  keepAliveAddr,
		LocalIP:           "127.0.0.1",
		DialTimeout:       2 * time.Second,
		KeepAliveInterval: 50 * time.Millisecond,
		RetryInterval:     20 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("NewTCPPunchMapper failed: %v", err)
	}

	externalPort, err := mapper.MapPort("TCP", internalPort, time.Minute)
	if err != nil {
		t.Fatalf("MapPort failed: %v", err)
	}
	if externalPort != 45678 {
		t.Fatalf("expected external port 45678, got %d", externalPort)
	}

	externalIP, err := mapper.GetExternalIP()
	if err != nil {
		t.Fatalf("GetExternalIP failed: %v", err)
	}
	if externalIP != "203.0.113.77" {
		t.Fatalf("expected external IP 203.0.113.77, got %s", externalIP)
	}

	listener, err := listenTCPContext(context.Background(), internalPort)
	if err != nil {
		t.Fatalf("listener should coexist with TCP punch keepalive socket: %v", err)
	}
	listener.Close()

	if err := mapper.UnmapPort("TCP", externalPort); err != nil {
		t.Fatalf("UnmapPort failed: %v", err)
	}
}

func TestTCPPunchMapperRejectsUDP(t *testing.T) {
	mapper, err := NewTCPPunchMapper(TCPPunchConfig{
		STUNAddress:      "127.0.0.1:1",
		KeepAliveAddress: "127.0.0.1:2",
		LocalIP:          "127.0.0.1",
	})
	if err != nil {
		t.Fatalf("NewTCPPunchMapper failed: %v", err)
	}

	if _, err := mapper.MapPort("UDP", 12345, time.Minute); err == nil {
		t.Fatalf("expected UDP MapPort to fail")
	}
	if err := mapper.UnmapPort("UDP", 12345); err == nil {
		t.Fatalf("expected UDP UnmapPort to fail")
	}
}

func TestTCPPunchMapperFromEnvDisabled(t *testing.T) {
	t.Setenv(envTCPPunchEnable, "")

	if _, err := newTCPPunchMapperFromEnv(); err != errTCPPunchDisabled {
		t.Fatalf("expected errTCPPunchDisabled, got %v", err)
	}
}

func startKeepAliveServer(t *testing.T) (string, func()) {
	t.Helper()

	ln, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to start keepalive server: %v", err)
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				_, _ = io.Copy(io.Discard, conn)
			}()
		}
	}()

	return ln.Addr().String(), func() {
		ln.Close()
		<-done
	}
}

func startSTUNServer(t *testing.T, mappedIP string, mappedPort int) (string, func()) {
	t.Helper()

	ln, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to start STUN server: %v", err)
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				request := make([]byte, 20)
				if _, err := io.ReadFull(conn, request); err != nil {
					return
				}
				response := buildSTUNResponse(t, request[8:20], mappedIP, mappedPort)
				_, _ = conn.Write(response)
			}()
		}
	}()

	return ln.Addr().String(), func() {
		ln.Close()
		<-done
	}
}

func buildSTUNResponse(t *testing.T, txID []byte, mappedIP string, mappedPort int) []byte {
	t.Helper()

	ip := net.ParseIP(mappedIP).To4()
	if ip == nil {
		t.Fatalf("invalid IPv4 address %s", mappedIP)
	}

	response := make([]byte, 32)
	binary.BigEndian.PutUint16(response[0:2], 0x0101)
	binary.BigEndian.PutUint16(response[2:4], 12)
	binary.BigEndian.PutUint32(response[4:8], 0x2112A442)
	copy(response[8:20], txID)
	binary.BigEndian.PutUint16(response[20:22], 0x0020)
	binary.BigEndian.PutUint16(response[22:24], 8)
	response[25] = 0x01
	binary.BigEndian.PutUint16(response[26:28], uint16(mappedPort^0x2112))
	cookie := []byte{0x21, 0x12, 0xA4, 0x42}
	for i := 0; i < 4; i++ {
		response[28+i] = ip[i] ^ cookie[i]
	}
	return response
}

func reserveTCPPort(t *testing.T) int {
	t.Helper()

	ln, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to reserve TCP port: %v", err)
	}
	defer ln.Close()

	return ln.Addr().(*net.TCPAddr).Port
}
