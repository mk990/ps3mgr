package cli

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"ps3mgr/internal/app"
	"ps3mgr/internal/config"
	"ps3mgr/internal/ps2"
)

type blockingCoverClient struct {
	started chan struct{}
	once    sync.Once
}

func (c *blockingCoverClient) Do(request *http.Request) (*http.Response, error) {
	c.once.Do(func() { close(c.started) })
	<-request.Context().Done()
	return nil, request.Context().Err()
}

func TestHelpAndUnknownCommandExitCodes(t *testing.T) {
	var output, errors bytes.Buffer
	runner := Runner{Out: &output, Err: &errors}
	if code := runner.Run(context.Background(), []string{"--help"}); code != 0 || !strings.Contains(output.String(), "local-games") {
		t.Fatalf("help code=%d output=%q", code, output.String())
	}
	if code := runner.Run(context.Background(), []string{"unknown"}); code != 2 {
		t.Fatalf("unknown command code=%d", code)
	}
}

func TestVersionCommand(t *testing.T) {
	previous := version
	version = "1.2.3-test"
	defer func() { version = previous }()

	var output bytes.Buffer
	code := (Runner{Out: &output, Err: &bytes.Buffer{}}).Run(context.Background(), []string{"version"})
	if code != 0 || output.String() != "ps3mgr 1.2.3-test\n" {
		t.Fatalf("code=%d output=%q", code, output.String())
	}
}

func TestLocalGamesFlagOverridesEnvironment(t *testing.T) {
	good := t.TempDir()
	if err := os.Mkdir(filepath.Join(good, "My Game"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PS3MGR_PS3_GAME_DIR", filepath.Join(good, "missing"))
	var output, errors bytes.Buffer
	code := (Runner{Out: &output, Err: &errors}).Run(context.Background(), []string{"local-games", "--dir", good, "--json"})
	if code != 0 || !strings.Contains(output.String(), "My Game") {
		t.Fatalf("code=%d output=%q errors=%q", code, output.String(), errors.String())
	}
}

func TestAddConsoleRejectsPublicAddress(t *testing.T) {
	var output, errors bytes.Buffer
	code := (Runner{Out: &output, Err: &errors}).Run(context.Background(), []string{"add-console", "--ip", "8.8.8.8"})
	if code != 1 || !strings.Contains(errors.String(), "private or local") {
		t.Fatalf("code=%d output=%q errors=%q", code, output.String(), errors.String())
	}
}

func TestPS2HelpAndLocalSizeOverride(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "SCES_517.19.Game.ISO"), []byte("fixture"), 0644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PS3MGR_PS2_GAME_DIR", filepath.Join(root, "missing"))
	t.Setenv("PS3MGR_PS2_COVER_DOWNLOAD", "false")
	var output, errors bytes.Buffer
	runner := Runner{Out: &output, Err: &errors}
	if code := runner.Run(context.Background(), []string{"ps2", "--help"}); code != 0 || !strings.Contains(output.String(), "ps2 install") {
		t.Fatalf("help code=%d output=%q", code, output.String())
	}
	output.Reset()
	errors.Reset()
	if code := runner.Run(context.Background(), []string{"ps2", "local-games", "--dir", root, "--size"}); code != 0 || !strings.Contains(output.String(), "PS2 ISO Size Report") {
		t.Fatalf("code=%d output=%q errors=%q", code, output.String(), errors.String())
	}
}

func TestPS4HelpAndLocalPackageOverride(t *testing.T) {
	root := t.TempDir()
	header := make([]byte, 0x100)
	binary.BigEndian.PutUint32(header[:4], 0x7f434e54)
	copy(header[0x40:], "UP0001-CUSA12345_00-ABCDEFGHIJKLMNOP")
	binary.BigEndian.PutUint32(header[0x74:0x78], 0x1a)
	if err := os.WriteFile(filepath.Join(root, "My Game.pkg"), header, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PS3MGR_PS4_GAME_DIR", filepath.Join(root, "missing"))
	var output, errors bytes.Buffer
	runner := Runner{Out: &output, Err: &errors}
	if code := runner.Run(context.Background(), []string{"ps4", "--help"}); code != 0 || !strings.Contains(output.String(), "Remote Package Installer") {
		t.Fatalf("help code=%d output=%q", code, output.String())
	}
	output.Reset()
	errors.Reset()
	if code := runner.Run(context.Background(), []string{"ps4", "local-games", "--dir", root}); code != 0 || !strings.Contains(output.String(), "CUSA12345") {
		t.Fatalf("code=%d output=%q errors=%q", code, output.String(), errors.String())
	}
}

func TestServeListensBeforeSlowLibraryInitialization(t *testing.T) {
	probe, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		if errors.Is(err, syscall.EPERM) {
			t.Skip("sandbox does not permit local TCP listeners")
		}
		t.Fatal(err)
	}
	address := probe.Addr().String()
	if err := probe.Close(); err != nil {
		t.Fatal(err)
	}

	ps2Root := t.TempDir()
	if err := os.WriteFile(filepath.Join(ps2Root, "SCES_517.19.Game.iso"), []byte("fixture"), 0o600); err != nil {
		t.Fatal(err)
	}
	application := app.New(config.Config{
		PS3GameDir:       t.TempDir(),
		PS2GameDir:       ps2Root,
		PS2SystemDir:     t.TempDir(),
		PS2USBRoot:       t.TempDir(),
		PS2CoverDownload: true,
		PS4GameDir:       t.TempDir(),
		PS5GameDir:       t.TempDir(),
		RemoteGameDir:    "/dev_hdd0/GAMES",
		Workers:          1,
		FTPTimeout:       time.Second,
	})
	coverClient := &blockingCoverClient{started: make(chan struct{})}
	application.PS2.Covers = &ps2.CoverCache{Client: coverClient, Workers: 1}

	ctx, cancel := context.WithCancel(context.Background())
	serveDone := make(chan error, 1)
	go func() {
		serveDone <- (Runner{Out: io.Discard, Err: io.Discard}).serve(ctx, application, []string{"--listen", address})
	}()

	select {
	case <-coverClient.started:
	case <-time.After(3 * time.Second):
		cancel()
		t.Fatal("slow startup scan did not begin")
	}

	client := &http.Client{Timeout: time.Second}
	response, err := client.Get("http://" + address + "/api/health")
	if err != nil {
		cancel()
		t.Fatalf("health endpoint was unavailable during startup scan: %v", err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusOK {
		cancel()
		t.Fatalf("health status during startup scan = %d", response.StatusCode)
	}

	cancel()
	select {
	case err := <-serveDone:
		if err != nil {
			t.Fatalf("serve shutdown: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("serve did not stop after cancellation")
	}
	if err := application.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
}
