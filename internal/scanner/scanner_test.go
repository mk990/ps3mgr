package scanner

import (
	"context"
	"net"
	"sync/atomic"
	"testing"
	"time"
)

type fakeDetector struct{ calls atomic.Int32 }

func (d *fakeDetector) Detect(_ context.Context, _ string) (bool, int, error) {
	d.calls.Add(1)
	return true, 7, nil
}

func TestEnumerateCIDR(t *testing.T) {
	addresses, err := Enumerate("192.168.7.0/30")
	if err != nil {
		t.Fatal(err)
	}
	if len(addresses) != 2 || addresses[0].String() != "192.168.7.1" || addresses[1].String() != "192.168.7.2" {
		t.Fatalf("unexpected addresses: %v", addresses)
	}
	for _, invalid := range []string{"bad", "8.8.8.0/24", "192.168.0.0/8", "2001:db8::/64"} {
		if _, err := Enumerate(invalid); err == nil {
			t.Fatalf("expected %q to be rejected", invalid)
		}
	}
}

func TestScanChecksTCPThenDetects(t *testing.T) {
	detector := &fakeDetector{}
	s := &Scanner{Detector: detector, Workers: 2, Timeout: time.Second, Dial: func(context.Context, string, string) (net.Conn, error) {
		client, server := net.Pipe()
		go server.Close()
		return client, nil
	}}
	items, err := s.Scan(context.Background(), "127.0.0.1/32", nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].IP != "127.0.0.1" || items[0].GameCount != 7 || detector.calls.Load() != 1 {
		t.Fatalf("unexpected scan: %+v, calls %d", items, detector.calls.Load())
	}
}

func TestScanCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := (&Scanner{Detector: &fakeDetector{}}).Scan(ctx, "127.0.0.1/32", nil)
	if err == nil {
		t.Fatal("expected cancellation")
	}
}
