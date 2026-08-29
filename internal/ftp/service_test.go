package ftp

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"ps3mgr/internal/domain"
)

func TestLooksLikePS3RequiresFilesystemMarkers(t *testing.T) {
	for _, roots := range [][]string{{"dev_hdd0"}, {"DEV_FLASH", "other"}} {
		if !looksLikePS3(roots) {
			t.Fatalf("expected PS3 markers in %v", roots)
		}
	}
	if looksLikePS3([]string{"pub", "uploads", "games"}) {
		t.Fatal("a generic FTP server must not be detected as a PS3")
	}
}

func TestCleanPathRemovesCommandInjection(t *testing.T) {
	if got := cleanPath("/dev_hdd0/GAMES\r\nDELE /file"); got != "/dev_hdd0/GAMESDELE /file" {
		t.Fatalf("unexpected clean path %q", got)
	}
}

func TestDownloadNameUsesTitleIDAndSanitizesTitle(t *testing.T) {
	game := domain.Game{ID: "CUSA12345", Title: "My/Game: Deluxe", RemotePath: "/data/games/CUSA12345"}
	if got := DownloadName(game); got != "CUSA12345 - My-Game- Deluxe" {
		t.Fatalf("download name = %q", got)
	}
}

func TestDownloadFileResumesPartFile(t *testing.T) {
	server := newDownloadFTPServer(t, []byte("hello world"), true, 0)
	defer server.Close()

	localPath := filepath.Join(t.TempDir(), "game.bin")
	if err := os.WriteFile(localPath+".part", []byte("hello "), 0o600); err != nil {
		t.Fatal(err)
	}
	var transferred int64
	service := Service{Port: server.Port(), User: "anonymous", Timeout: time.Second}
	if err := service.DownloadFile(context.Background(), "127.0.0.1", "/game.bin", localPath, func(progress Progress) {
		transferred += progress.Delta
	}); err != nil {
		t.Fatalf("resume download: %v", err)
	}
	assertFileContents(t, localPath, "hello world")
	if transferred != 11 {
		t.Fatalf("reported transferred bytes = %d, want 11", transferred)
	}
	server.assertOffsets(t, []int64{6}, []int64{6})
}

func TestDownloadFileRestartsWhenResumeUnsupported(t *testing.T) {
	server := newDownloadFTPServer(t, []byte("hello world"), false, 0)
	defer server.Close()

	localPath := filepath.Join(t.TempDir(), "game.bin")
	if err := os.WriteFile(localPath+".part", []byte("hello "), 0o600); err != nil {
		t.Fatal(err)
	}
	var transferred int64
	service := Service{Port: server.Port(), User: "anonymous", Timeout: time.Second}
	if err := service.DownloadFile(context.Background(), "127.0.0.1", "/game.bin", localPath, func(progress Progress) {
		transferred += progress.Delta
	}); err != nil {
		t.Fatalf("fallback download: %v", err)
	}
	assertFileContents(t, localPath, "hello world")
	if transferred != 11 {
		t.Fatalf("reported transferred bytes = %d, want 11", transferred)
	}
	server.assertOffsets(t, []int64{6}, []int64{0})
}

func TestDownloadFilePreservesPartAfterInterruption(t *testing.T) {
	server := newDownloadFTPServer(t, []byte("hello world"), true, 2)
	defer server.Close()

	localPath := filepath.Join(t.TempDir(), "game.bin")
	if err := os.WriteFile(localPath+".part", []byte("hello "), 0o600); err != nil {
		t.Fatal(err)
	}
	service := Service{Port: server.Port(), User: "anonymous", Timeout: time.Second}
	if err := service.DownloadFile(context.Background(), "127.0.0.1", "/game.bin", localPath, nil); err == nil {
		t.Fatal("interrupted download unexpectedly succeeded")
	}
	assertFileContents(t, localPath+".part", "hello wo")
	if _, err := os.Stat(localPath); !os.IsNotExist(err) {
		t.Fatalf("finished path exists after interrupted download: %v", err)
	}
}

func TestDownloadFileRejectsSizeMismatch(t *testing.T) {
	server := newDownloadFTPServer(t, []byte("hello world"), true, 0)
	server.reportedSize++
	defer server.Close()

	localPath := filepath.Join(t.TempDir(), "game.bin")
	service := Service{Port: server.Port(), User: "anonymous", Timeout: time.Second}
	err := service.DownloadFile(context.Background(), "127.0.0.1", "/game.bin", localPath, nil)
	if err == nil || !strings.Contains(err.Error(), "download integrity check failed") {
		t.Fatalf("size mismatch error = %v", err)
	}
	assertFileContents(t, localPath+".part", "hello world")
	if _, err := os.Stat(localPath); !os.IsNotExist(err) {
		t.Fatalf("finished path exists after integrity failure: %v", err)
	}
}

func TestVerifyTransferSize(t *testing.T) {
	if err := verifyTransferSize("upload", "/remote/game.bin", 12, 12); err != nil {
		t.Fatalf("matching size rejected: %v", err)
	}
	if err := verifyTransferSize("upload", "/remote/game.bin", 11, 12); err == nil || !strings.Contains(err.Error(), "upload integrity check failed") {
		t.Fatalf("mismatching size error = %v", err)
	}
}

func TestUploadGameDisablesSELFSizeView(t *testing.T) {
	server := newDownloadFTPServer(t, nil, true, 0)
	server.dynamicSize = true
	server.selfMode = true
	server.selfReportedSize = 7
	defer server.Close()

	localPath := filepath.Join(t.TempDir(), "eboot.bin")
	want := []byte("complete signed executable data")
	if err := os.WriteFile(localPath, want, 0o600); err != nil {
		t.Fatal(err)
	}
	service := Service{
		Port:                  server.Port(),
		User:                  "anonymous",
		Timeout:               time.Second,
		DisableSELFDecryption: true,
	}
	game := domain.Game{Title: "Test game", LocalPath: localPath}
	if err := service.UploadGame(context.Background(), "127.0.0.1", game, "/games", nil); err != nil {
		t.Fatalf("upload raw SELF: %v", err)
	}
	server.assertUpload(t, want, []int64{0}, 1)
}

func TestNamesFallsBackToCWDForPS4FTPServers(t *testing.T) {
	server := newDownloadFTPServer(t, nil, true, 0)
	server.names = []string{"CUSA00001", "CUSA00002"}
	server.rejectNLSTPath = true
	defer server.Close()

	client, err := Dial(context.Background(), net.JoinHostPort("127.0.0.1", server.Port()), "anonymous", "", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	names, err := client.Names(context.Background(), "/user/app")
	if err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(names) != "[CUSA00001 CUSA00002]" {
		t.Fatalf("names = %v", names)
	}
}

func TestNamesFallsBackToLISTWhenNLSTUnsupported(t *testing.T) {
	server := newDownloadFTPServer(t, nil, true, 0)
	server.names = []string{"CUSA00001", "CUSA00002"}
	server.disableNLST = true
	defer server.Close()

	client, err := Dial(context.Background(), net.JoinHostPort("127.0.0.1", server.Port()), "anonymous", "", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	names, err := client.Names(context.Background(), "/user/app")
	if err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(names) != "[CUSA00001 CUSA00002]" {
		t.Fatalf("names = %v", names)
	}
}

func assertFileContents(t *testing.T, path, want string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != want {
		t.Fatalf("%s = %q, want %q", filepath.Base(path), data, want)
	}
}

type downloadFTPServer struct {
	listener         net.Listener
	payload          []byte
	resume           bool
	interruptAfter   int
	reportedSize     int
	wg               sync.WaitGroup
	mu               sync.Mutex
	restOffsets      []int64
	retrOffsets      []int64
	names            []string
	rejectNLSTPath   bool
	disableNLST      bool
	dynamicSize      bool
	selfMode         bool
	selfReportedSize int
	selfCommands     int
	storOffsets      []int64
}

func newDownloadFTPServer(t *testing.T, payload []byte, resume bool, interruptAfter int) *downloadFTPServer {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := &downloadFTPServer{listener: listener, payload: payload, resume: resume, interruptAfter: interruptAfter, reportedSize: len(payload)}
	server.wg.Add(1)
	go server.serve()
	return server
}

func (s *downloadFTPServer) Port() string {
	return strconv.Itoa(s.listener.Addr().(*net.TCPAddr).Port)
}

func (s *downloadFTPServer) Close() {
	_ = s.listener.Close()
	s.wg.Wait()
}

func (s *downloadFTPServer) assertOffsets(t *testing.T, rest, retr []int64) {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	if fmt.Sprint(s.restOffsets) != fmt.Sprint(rest) {
		t.Fatalf("REST offsets = %v, want %v", s.restOffsets, rest)
	}
	if fmt.Sprint(s.retrOffsets) != fmt.Sprint(retr) {
		t.Fatalf("RETR offsets = %v, want %v", s.retrOffsets, retr)
	}
}

func (s *downloadFTPServer) assertUpload(t *testing.T, payload []byte, stor []int64, selfCommands int) {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	if string(s.payload) != string(payload) {
		t.Fatalf("uploaded payload = %q, want %q", s.payload, payload)
	}
	if fmt.Sprint(s.storOffsets) != fmt.Sprint(stor) {
		t.Fatalf("STOR offsets = %v, want %v", s.storOffsets, stor)
	}
	if s.selfCommands != selfCommands {
		t.Fatalf("SELF commands = %d, want %d", s.selfCommands, selfCommands)
	}
}

func (s *downloadFTPServer) serve() {
	defer s.wg.Done()
	for {
		conn, err := s.listener.Accept()
		if err != nil {
			return
		}
		s.wg.Add(1)
		go s.serveControl(conn)
	}
}

func (s *downloadFTPServer) serveControl(conn net.Conn) {
	defer s.wg.Done()
	defer conn.Close()
	reader := bufio.NewScanner(conn)
	writer := bufio.NewWriter(conn)
	respond := func(code int, message string) {
		_, _ = fmt.Fprintf(writer, "%d %s\r\n", code, message)
		_ = writer.Flush()
	}
	respond(220, "test FTP ready")
	var dataListener net.Listener
	defer func() {
		if dataListener != nil {
			_ = dataListener.Close()
		}
	}()
	var offset int64
	for reader.Scan() {
		fields := strings.SplitN(reader.Text(), " ", 2)
		command := strings.ToUpper(fields[0])
		argument := ""
		if len(fields) == 2 {
			argument = fields[1]
		}
		switch command {
		case "USER":
			respond(331, "password required")
		case "PASS":
			respond(230, "logged in")
		case "TYPE":
			respond(200, "binary mode")
		case "SIZE":
			s.mu.Lock()
			reportedSize := s.reportedSize
			if s.dynamicSize {
				if s.selfMode {
					reportedSize = s.selfReportedSize
				} else {
					reportedSize = len(s.payload)
				}
			}
			s.mu.Unlock()
			respond(213, strconv.Itoa(reportedSize))
		case "SELF":
			s.mu.Lock()
			s.selfMode = !s.selfMode
			s.selfCommands++
			enabled := s.selfMode
			s.mu.Unlock()
			if enabled {
				respond(226, "SELF transfer mode enabled")
			} else {
				respond(226, "SELF transfer mode disabled")
			}
		case "REST":
			requested, _ := strconv.ParseInt(argument, 10, 64)
			s.mu.Lock()
			s.restOffsets = append(s.restOffsets, requested)
			s.mu.Unlock()
			if !s.resume {
				respond(502, "REST unsupported")
				continue
			}
			offset = requested
			respond(350, "restart accepted")
		case "EPSV":
			var err error
			dataListener, err = net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				respond(425, "cannot open data connection")
				continue
			}
			port := dataListener.Addr().(*net.TCPAddr).Port
			respond(229, fmt.Sprintf("Entering Extended Passive Mode (|||%d|)", port))
		case "CWD":
			if argument == "/user/app" {
				respond(250, "directory changed")
			} else {
				respond(550, "directory unavailable")
			}
		case "NLST":
			if s.disableNLST {
				respond(502, "command not recognized")
				_ = dataListener.Close()
				dataListener = nil
				continue
			}
			if s.rejectNLSTPath && argument != "" {
				respond(550, "path argument unsupported")
				_ = dataListener.Close()
				dataListener = nil
				continue
			}
			respond(150, "opening data connection")
			dataConn, err := dataListener.Accept()
			if err != nil {
				return
			}
			_, _ = io.WriteString(dataConn, strings.Join(s.names, "\r\n")+"\r\n")
			_ = dataConn.Close()
			_ = dataListener.Close()
			dataListener = nil
			respond(226, "transfer complete")
		case "LIST":
			respond(150, "opening data connection")
			dataConn, err := dataListener.Accept()
			if err != nil {
				return
			}
			lines := make([]string, len(s.names))
			for i, name := range s.names {
				lines[i] = fmt.Sprintf("-rwxr-xr-x 1 root root 4096 Jan 01 00:00 %s", name)
			}
			_, _ = io.WriteString(dataConn, strings.Join(lines, "\r\n")+"\r\n")
			_ = dataConn.Close()
			_ = dataListener.Close()
			dataListener = nil
			respond(226, "transfer complete")
		case "RETR":
			s.mu.Lock()
			s.retrOffsets = append(s.retrOffsets, offset)
			s.mu.Unlock()
			respond(150, "opening data connection")
			dataConn, err := dataListener.Accept()
			if err != nil {
				return
			}
			payload := s.payload[offset:]
			interrupted := s.interruptAfter > 0 && s.interruptAfter < len(payload)
			if interrupted {
				payload = payload[:s.interruptAfter]
			}
			_, _ = dataConn.Write(payload)
			_ = dataConn.Close()
			_ = dataListener.Close()
			dataListener = nil
			if interrupted {
				respond(426, "transfer interrupted")
			} else {
				respond(226, "transfer complete")
			}
		case "STOR":
			s.mu.Lock()
			s.storOffsets = append(s.storOffsets, offset)
			s.mu.Unlock()
			respond(150, "opening data connection")
			dataConn, err := dataListener.Accept()
			if err != nil {
				return
			}
			stored, err := io.ReadAll(dataConn)
			_ = dataConn.Close()
			_ = dataListener.Close()
			dataListener = nil
			if err != nil {
				respond(426, "transfer interrupted")
				continue
			}
			s.mu.Lock()
			end := int(offset) + len(stored)
			payload := make([]byte, end)
			copy(payload, s.payload)
			copy(payload[int(offset):], stored)
			s.payload = payload
			s.mu.Unlock()
			offset = 0
			respond(226, "transfer complete")
		case "QUIT":
			respond(221, "goodbye")
			return
		default:
			respond(502, "command unsupported")
		}
	}
}
