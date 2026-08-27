package scanner

import (
	"context"
	"fmt"
	"net"
	"net/netip"
	"sort"
	"sync"
	"time"

	"ps3mgr/internal/domain"
)

type Detector interface {
	Detect(ctx context.Context, ip string) (detected bool, gameCount int, err error)
}

type Scanner struct {
	Detector Detector
	Workers  int
	Timeout  time.Duration
	Port     string
	Dial     func(ctx context.Context, network, address string) (net.Conn, error)
}

func Enumerate(cidr string) ([]netip.Addr, error) {
	prefix, err := netip.ParsePrefix(cidr)
	if err != nil || !prefix.Addr().Is4() {
		return nil, fmt.Errorf("CIDR must be a valid IPv4 network")
	}
	prefix = prefix.Masked()
	if !prefix.Addr().IsPrivate() && !prefix.Addr().IsLoopback() && !prefix.Addr().IsLinkLocalUnicast() {
		return nil, fmt.Errorf("CIDR must describe a local or private network")
	}
	bits := prefix.Bits()
	if bits < 16 {
		return nil, fmt.Errorf("CIDR contains too many hosts; use /16 or smaller")
	}
	addresses := make([]netip.Addr, 0, 1<<(32-bits))
	for addr := prefix.Addr(); prefix.Contains(addr); addr = addr.Next() {
		addresses = append(addresses, addr)
	}
	if len(addresses) > 2 && bits <= 30 {
		addresses = addresses[1 : len(addresses)-1]
	}
	return addresses, nil
}

func (s *Scanner) Scan(ctx context.Context, cidr string, found func(domain.Console)) ([]domain.Console, error) {
	addresses, err := Enumerate(cidr)
	if err != nil {
		return nil, err
	}
	workers := s.Workers
	if workers < 1 {
		workers = 16
	}
	timeout := s.Timeout
	if timeout <= 0 {
		timeout = 3 * time.Second
	}
	port := s.Port
	if port == "" {
		port = "21"
	}
	dial := s.Dial
	if dial == nil {
		dialer := &net.Dialer{}
		dial = dialer.DialContext
	}
	jobs := make(chan netip.Addr)
	results := make(chan domain.Console)
	var wait sync.WaitGroup
	for i := 0; i < workers; i++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			for address := range jobs {
				host := address.String()
				hostCtx, cancel := context.WithTimeout(ctx, timeout)
				conn, dialErr := dial(hostCtx, "tcp", net.JoinHostPort(host, port))
				if dialErr == nil {
					conn.Close()
					detected, count, detectErr := s.Detector.Detect(hostCtx, host)
					if detectErr == nil && detected {
						console := domain.Console{ID: host, IP: host, FTPOnline: true, Detected: true, GameCount: count, LastSeen: time.Now()}
						select {
						case results <- console:
						case <-ctx.Done():
						}
					}
				}
				cancel()
			}
		}()
	}
	go func() {
		defer close(jobs)
		for _, address := range addresses {
			select {
			case jobs <- address:
			case <-ctx.Done():
				return
			}
		}
	}()
	go func() { wait.Wait(); close(results) }()
	var consoles []domain.Console
	for console := range results {
		consoles = append(consoles, console)
		if found != nil {
			found(console)
		}
	}
	if err := ctx.Err(); err != nil {
		return consoles, err
	}
	sort.Slice(consoles, func(i, j int) bool { return consoles[i].IP < consoles[j].IP })
	return consoles, nil
}
