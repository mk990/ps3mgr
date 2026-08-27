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

type contextDetector struct{ calls atomic.Int32 }

func (d *contextDetector) Detect(ctx context.Context, _ string) (bool, int, error) {
	d.calls.Add(1)
	select {
	case <-time.After(15 * time.Millisecond):
		return true, 3, nil
	case <-ctx.Done():
		return false, 0, ctx.Err()
	}
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

func TestScanUsesShortProbeAndFreshDetectionTimeout(t *testing.T) {
	detector := &contextDetector{}
	s := &Scanner{
		Detector: detector, Workers: 1, Timeout: time.Millisecond,
		DetectionTimeout: 100 * time.Millisecond,
		Dial: func(context.Context, string, string) (net.Conn, error) {
			client, server := net.Pipe()
			go server.Close()
			return client, nil
		},
	}
	items, err := s.Scan(context.Background(), "127.0.0.1/32", nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || detector.calls.Load() != 1 {
		t.Fatalf("FTP detection reused the expired probe context: %+v", items)
	}
}

func TestScanBoundsUnreachableHostTime(t *testing.T) {
	s := &Scanner{
		Detector: &fakeDetector{}, Workers: 2, Timeout: 10 * time.Millisecond,
		Dial: func(ctx context.Context, _, _ string) (net.Conn, error) {
			<-ctx.Done()
			return nil, ctx.Err()
		},
	}
	started := time.Now()
	items, err := s.Scan(context.Background(), "127.0.0.0/30", nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 0 || time.Since(started) > 100*time.Millisecond {
		t.Fatalf("bounded scan was too slow: %v", time.Since(started))
	}
}
