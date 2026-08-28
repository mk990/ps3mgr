package ps5

import (
	"context"
	"fmt"
	"net"
	"path"
	"sort"
	"strconv"
	"strings"
	"time"

	"ps3mgr/internal/domain"
	psftp "ps3mgr/internal/ftp"
)

const DefaultFTPPort = 2121

type FTP struct {
	User       string
	Password   string
	Timeout    time.Duration
	Port       int
	RemoteRoot string
	uploader   *psftp.Service
}

func NewFTP(user, password string, timeout time.Duration, port int, remoteRoot string) *FTP {
	if port == 0 {
		port = DefaultFTPPort
	}
	f := &FTP{User: user, Password: password, Timeout: timeout, Port: port, RemoteRoot: remoteRoot}
	f.uploader = &psftp.Service{User: user, Password: password, Timeout: timeout, Port: strconv.Itoa(port), RemoteRoot: remoteRoot}
	return f
}

func (f *FTP) connect(ctx context.Context, ip string) (*psftp.Client, error) {
	return psftp.Dial(ctx, net.JoinHostPort(ip, strconv.Itoa(f.Port)), f.User, f.Password, f.Timeout)
}

func (f *FTP) Detect(ctx context.Context, ip string) (bool, int, error) {
	client, err := f.connect(ctx, ip)
	if err != nil {
		return false, 0, err
	}
	defer client.Close()
	roots, err := client.Names(ctx, "/")
	if err != nil || !containsFold(roots, "data") {
		return false, 0, err
	}
	data, err := client.Names(ctx, "/data")
	if err != nil || !containsFold(data, "etaHEN") {
		return false, 0, err
	}
	names, err := client.Names(ctx, f.RemoteRoot)
	if err != nil {
		return true, 0, nil
	}
	return true, len(names), nil
}

func (f *FTP) RemoteGames(ctx context.Context, ip string) ([]domain.Game, error) {
	client, err := f.connect(ctx, ip)
	if err != nil {
		return nil, err
	}
	defer client.Close()
	names, err := client.Names(ctx, f.RemoteRoot)
	if err != nil {
		return nil, fmt.Errorf("list ShadowMountPlus games: %w", err)
	}
	result := make([]domain.Game, 0, len(names))
	for _, name := range names {
		if strings.EqualFold(name, "backports") {
			continue
		}
		format := imageFormats[strings.ToLower(path.Ext(name))]
		if format == "" {
			format = "folder"
		}
		id := titleID(name)
		title := titleFromFilename(name, id)
		if format == "folder" {
			if data, readErr := client.ReadFile(ctx, path.Join(f.RemoteRoot, name, "sce_sys/param.json"), 1<<20); readErr == nil {
				if metadata, parseErr := parseParam(data); parseErr == nil {
					if metadata.ID != "" {
						id = metadata.ID
					}
					if metadata.Title != "" {
						title = metadata.Title
					}
				}
			}
		}
		result = append(result, domain.Game{
			ID: id, Title: title, Format: format,
			RemotePath: path.Join(f.RemoteRoot, name), Installed: true, State: domain.StateInstalled,
		})
	}
	sort.Slice(result, func(i, j int) bool { return strings.ToLower(result[i].Title) < strings.ToLower(result[j].Title) })
	return result, nil
}

func (f *FTP) UploadGame(ctx context.Context, ip string, game domain.Game, remoteRoot string, progress func(psftp.Progress)) error {
	return f.uploader.UploadGame(ctx, ip, game, remoteRoot, progress)
}

func (f *FTP) DownloadGame(ctx context.Context, ip string, game domain.Game, localRoot string, progress func(psftp.Progress)) error {
	return f.uploader.DownloadGame(ctx, ip, game, localRoot, progress)
}

func containsFold(values []string, wanted string) bool {
	for _, value := range values {
		if strings.EqualFold(value, wanted) {
			return true
		}
	}
	return false
}
