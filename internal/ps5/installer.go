package ps5

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"mime/multipart"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"ps3mgr/internal/domain"
	"ps3mgr/internal/ftp"
)

// etaHEN DPIv2 accepts a multipart file at POST /upload. Its success reply
// confirms that the console started an installation task.
type consoleUploader struct {
	ftp     *FTP
	dpiPort string
}

func (u consoleUploader) UploadGame(ctx context.Context, ip string, game domain.Game, remoteRoot string, progress func(ftp.Progress)) error {
	if game.Format != "pkg" {
		return u.ftp.UploadGame(ctx, ip, game, remoteRoot, progress)
	}
	url := "http://" + net.JoinHostPort(ip, u.dpiPort) + "/upload"
	return uploadPackage(ctx, url, game.LocalPath, progress)
}

var dpiClient = &http.Client{
	Transport: &http.Transport{
		DialContext:           (&net.Dialer{Timeout: 10 * time.Second}).DialContext,
		ResponseHeaderTimeout: 2 * time.Minute,
		DisableKeepAlives:     true,
	},
	CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
}

type progressReader struct {
	io.Reader
	filename string
	report   func(ftp.Progress)
}

func (r progressReader) Read(buf []byte) (int, error) {
	n, err := r.Reader.Read(buf)
	if n > 0 && r.report != nil {
		r.report(ftp.Progress{File: r.filename, Delta: int64(n)})
	}
	return n, err
}

func uploadPackage(ctx context.Context, url, path string, progress func(ftp.Progress)) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Size() == 0 {
		return fmt.Errorf("empty or invalid PS5 package %q", path)
	}
	// Keep only the multipart framing in memory, even for multi-GiB packages.
	var framing bytes.Buffer
	form := multipart.NewWriter(&framing)
	name := filepath.Base(path)
	headerName := strings.Map(func(r rune) rune {
		if r < ' ' || r == 0x7f {
			return '_'
		}
		return r
	}, name)
	if _, err := form.CreateFormFile("file", headerName); err != nil {
		return err
	}
	headerLen := framing.Len()
	if err := form.Close(); err != nil {
		return err
	}
	payload := progressReader{io.LimitReader(file, info.Size()), name, progress}
	body := io.MultiReader(bytes.NewReader(framing.Bytes()[:headerLen]), payload, bytes.NewReader(framing.Bytes()[headerLen:]))
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, body)
	if err != nil {
		return err
	}
	req.ContentLength = info.Size() + int64(framing.Len())
	req.Header.Set("Content-Type", form.FormDataContentType())
	response, err := dpiClient.Do(req)
	if err != nil {
		return fmt.Errorf("etaHEN Direct Package Installer v2 at %s: %w", url, err)
	}
	defer response.Body.Close()
	reply, err := io.ReadAll(io.LimitReader(response.Body, 4096))
	if err != nil {
		return fmt.Errorf("read etaHEN package installer reply: %w", err)
	}
	message := strings.TrimSpace(string(reply))
	if response.StatusCode != http.StatusOK || !strings.HasPrefix(message, "SUCCESS:") {
		return fmt.Errorf("etaHEN package installer returned %s: %s", response.Status, message)
	}
	return nil
}
