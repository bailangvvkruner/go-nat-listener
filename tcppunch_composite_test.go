package nattraversal

import (
	"fmt"
	"testing"
	"time"
)

func TestTCPPunchPortMapperMapsInnerThenPunch(t *testing.T) {
	inner := &recordingPortMapper{externalPort: 24567, externalIP: "172.19.7.253"}
	punch := &recordingPortMapper{externalPort: 59863, externalIP: "223.73.225.207"}

	mapper, err := NewTCPPunchPortMapper(inner, punch)
	if err != nil {
		t.Fatalf("NewTCPPunchPortMapper failed: %v", err)
	}

	externalPort, err := mapper.MapPort("TCP", 24567, time.Minute)
	if err != nil {
		t.Fatalf("MapPort failed: %v", err)
	}
	if externalPort != 59863 {
		t.Fatalf("expected public punch port 59863, got %d", externalPort)
	}

	externalIP, err := mapper.GetExternalIP()
	if err != nil {
		t.Fatalf("GetExternalIP failed: %v", err)
	}
	if externalIP != "223.73.225.207" {
		t.Fatalf("expected punch external IP, got %s", externalIP)
	}

	assertMapCall(t, inner.mapCalls, 0, "TCP", 24567, time.Minute)
	assertMapCall(t, punch.mapCalls, 0, "TCP", 24567, time.Minute)

	if err := mapper.UnmapPort("TCP", 59863); err != nil {
		t.Fatalf("UnmapPort failed: %v", err)
	}
	assertUnmapCall(t, punch.unmapCalls, 0, "TCP", 59863)
	assertUnmapCall(t, inner.unmapCalls, 0, "TCP", 24567)
}

func TestTCPPunchPortMapperCleansInnerMappingOnPunchFailure(t *testing.T) {
	inner := &recordingPortMapper{externalPort: 24567}
	punch := &recordingPortMapper{mapErr: fmt.Errorf("stun failed")}

	mapper, err := NewTCPPunchPortMapper(inner, punch)
	if err != nil {
		t.Fatalf("NewTCPPunchPortMapper failed: %v", err)
	}

	if _, err := mapper.MapPort("TCP", 24567, time.Minute); err == nil {
		t.Fatalf("expected MapPort to fail")
	}

	assertMapCall(t, inner.mapCalls, 0, "TCP", 24567, time.Minute)
	assertMapCall(t, punch.mapCalls, 0, "TCP", 24567, time.Minute)
	assertUnmapCall(t, inner.unmapCalls, 0, "TCP", 24567)
}

func TestTCPPunchPortMapperRenewsPublicPort(t *testing.T) {
	inner := &recordingPortMapper{externalPort: 24567}
	punch := &recordingPortMapper{externalPort: 59863, externalIP: "223.73.225.207"}

	mapper, err := NewTCPPunchPortMapper(inner, punch)
	if err != nil {
		t.Fatalf("NewTCPPunchPortMapper failed: %v", err)
	}

	if port, err := mapper.MapPort("TCP", 24567, time.Minute); err != nil || port != 59863 {
		t.Fatalf("initial MapPort got port=%d err=%v", port, err)
	}

	punch.externalPort = 59864
	if port, err := mapper.MapPort("TCP", 24567, time.Minute); err != nil || port != 59864 {
		t.Fatalf("renewal MapPort got port=%d err=%v", port, err)
	}

	if err := mapper.UnmapPort("TCP", 59864); err != nil {
		t.Fatalf("UnmapPort failed: %v", err)
	}
	assertUnmapCall(t, punch.unmapCalls, 0, "TCP", 59864)
	assertUnmapCall(t, inner.unmapCalls, 0, "TCP", 24567)
}

func TestTCPPunchPortMapperRejectsUDP(t *testing.T) {
	mapper, err := NewTCPPunchPortMapper(&recordingPortMapper{}, &recordingPortMapper{})
	if err != nil {
		t.Fatalf("NewTCPPunchPortMapper failed: %v", err)
	}

	if _, err := mapper.MapPort("UDP", 24567, time.Minute); err == nil {
		t.Fatalf("expected UDP MapPort to fail")
	}
	if err := mapper.UnmapPort("UDP", 24567); err == nil {
		t.Fatalf("expected UDP UnmapPort to fail")
	}
}

type recordingPortMapper struct {
	externalPort int
	externalIP   string
	mapErr       error
	unmapErr     error
	getIPErr     error

	mapCalls   []recordedMapCall
	unmapCalls []recordedUnmapCall
}

type recordedMapCall struct {
	protocol     string
	internalPort int
	duration     time.Duration
}

type recordedUnmapCall struct {
	protocol     string
	externalPort int
}

func (m *recordingPortMapper) MapPort(protocol string, internalPort int, duration time.Duration) (int, error) {
	m.mapCalls = append(m.mapCalls, recordedMapCall{
		protocol:     protocol,
		internalPort: internalPort,
		duration:     duration,
	})
	if m.mapErr != nil {
		return 0, m.mapErr
	}
	return m.externalPort, nil
}

func (m *recordingPortMapper) UnmapPort(protocol string, externalPort int) error {
	m.unmapCalls = append(m.unmapCalls, recordedUnmapCall{
		protocol:     protocol,
		externalPort: externalPort,
	})
	return m.unmapErr
}

func (m *recordingPortMapper) GetExternalIP() (string, error) {
	if m.getIPErr != nil {
		return "", m.getIPErr
	}
	return m.externalIP, nil
}

func assertMapCall(t *testing.T, calls []recordedMapCall, index int, protocol string, internalPort int, duration time.Duration) {
	t.Helper()
	if len(calls) <= index {
		t.Fatalf("expected map call %d, got %d calls", index, len(calls))
	}
	call := calls[index]
	if call.protocol != protocol || call.internalPort != internalPort || call.duration != duration {
		t.Fatalf("unexpected map call %d: %+v", index, call)
	}
}

func assertUnmapCall(t *testing.T, calls []recordedUnmapCall, index int, protocol string, externalPort int) {
	t.Helper()
	if len(calls) <= index {
		t.Fatalf("expected unmap call %d, got %d calls", index, len(calls))
	}
	call := calls[index]
	if call.protocol != protocol || call.externalPort != externalPort {
		t.Fatalf("unexpected unmap call %d: %+v", index, call)
	}
}
