package ps5

import (
	"context"
	"fmt"
	"hash/fnv"
	"net"
	"net/http"
	"os"
	"path"
	"path/filepath"
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
	f.uploader = &psftp.Service{User: user, Password: password, Timeout: timeout, Port: strconv.Itoa(port), RemoteRoot: remoteRoot, DisableSELFDecryption: true}
	return f
}

func (f *FTP) connect(ctx context.Context, ip string) (*psftp.Client, error) {
	client, err := psftp.Dial(ctx, net.JoinHostPort(ip, strconv.Itoa(f.Port)), f.User, f.Password, f.Timeout)
	if err != nil {
		return nil, err
	}
	if err := client.DisableSELFDecryption(ctx); err != nil {
		client.Close()
		return nil, fmt.Errorf("configure raw FTP transfers: %w", err)
	}
	return client, nil
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
	artwork := make(map[string]string)
	for _, name := range names {
		if isAvatarFile(name) {
			artwork[strings.ToLower(name)] = name
		}
	}
	for _, name := range names {
		if strings.EqualFold(name, "backports") || isAvatarFile(name) {
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
		game := domain.Game{
			ID: id, Title: title, Format: format,
			RemotePath: path.Join(f.RemoteRoot, name), Installed: true, State: domain.StateInstalled,
		}
		if format != "folder" {
			stem := strings.TrimSuffix(name, path.Ext(name))
			for _, extension := range coverExtensions {
				if icon := artwork[strings.ToLower(stem+extension)]; icon != "" {
					game.IconPath = path.Join(f.RemoteRoot, icon)
					break
				}
			}
		}
		result = append(result, game)
	}
	sort.Slice(result, func(i, j int) bool { return strings.ToLower(result[i].Title) < strings.ToLower(result[j].Title) })
	return result, nil
}

func (f *FTP) UploadGame(ctx context.Context, ip string, game domain.Game, remoteRoot string, progress func(psftp.Progress)) error {
	return f.uploader.UploadGame(ctx, ip, game, remoteRoot, progress)
}

func (f *FTP) DownloadGame(ctx context.Context, ip string, game domain.Game, localRoot string, progress func(psftp.Progress)) error {
	if game.Format == "folder" || imageFormats[strings.ToLower(path.Ext(game.RemotePath))] == "" {
		return f.uploader.DownloadGame(ctx, ip, game, localRoot, progress)
	}
	name := psftp.DownloadName(domain.Game{Title: path.Base(game.RemotePath)})
	if !filepath.IsLocal(name) {
		return fmt.Errorf("invalid PS5 image filename %q", name)
	}
	if err := f.uploader.DownloadFile(ctx, ip, game.RemotePath, filepath.Join(localRoot, name), progress); err != nil {
		return err
	}
	f.pullAvatar(ctx, ip, game, localRoot, name)
	return ctx.Err()
}

func isAvatarFile(name string) bool {
	for _, extension := range coverExtensions {
		if strings.EqualFold(path.Ext(name), extension) {
			return true
		}
	}
	return false
}

// Artwork is optional. Preserve a completed game when the console has no icon.
func (f *FTP) pullAvatar(ctx context.Context, ip string, game domain.Game, root, name string) {
	if imageAvatarURL(ctx, root, name) != "" {
		return
	}
	var sources []string
	if game.IconPath != "" {
		sources = append(sources, game.IconPath)
	}
	if id := titleID(game.ID); id != "" {
		sources = append(sources, path.Join("/user/appmeta", id, "icon0.png"))
	}
	switch game.Format {
	case "ffpkg", "exfat", "ffpfs":
		sources = append(sources, mountedIcon(game.RemotePath))
	}
	if len(sources) == 0 && game.Format != "ffpfsc" {
		return
	}
	client, err := f.connect(ctx, ip)
	if err != nil {
		return
	}
	defer client.Close()
	data, ext := readAvatar(ctx, client, sources)
	if data == nil && game.Format == "ffpfsc" {
		// ShadowMountPlus mounts the container first, then mounts its inner
		// image separately. The game artwork lives on that second mount.
		container := imageMount("/mnt/shadowmnt/pfsc", game.RemotePath)
		names, err := client.Names(ctx, container)
		if err != nil {
			return
		}
		var nested []string
		for _, name := range names {
			ext := strings.ToLower(path.Ext(name))
			if strings.EqualFold(name, "pfs_image.dat") || ext == ".ffpfs" || ext == ".ffpkg" || ext == ".exfat" {
				nested = append(nested, mountedIcon(path.Join(container, name)))
			}
		}
		data, ext = readAvatar(ctx, client, nested)
	}
	if data == nil {
		return
	}
	icon, err := os.CreateTemp(root, ".ps5-avatar-*")
	if err != nil {
		return
	}
	defer os.Remove(icon.Name())
	_, writeErr := icon.Write(data)
	if closeErr := icon.Close(); writeErr == nil && closeErr == nil {
		_ = os.Rename(icon.Name(), filepath.Join(root, strings.TrimSuffix(name, filepath.Ext(name))+ext))
	}
}

func mountedIcon(source string) string {
	return path.Join(imageMount("/mnt/shadowmnt", source), "sce_sys/icon0.png")
}

func imageMount(root, source string) string {
	hash := fnv.New32a()
	_, _ = hash.Write([]byte(source))
	filename := path.Base(source)
	stem := strings.TrimSuffix(filename, path.Ext(filename))
	// ShadowMountPlus fits the full path and terminating NUL into the
	// PS5 statfs mount-name field (88 bytes), preserving the hash suffix.
	stem = stem[:min(len(stem), 88-len(root)-11)]
	return path.Join(root, fmt.Sprintf("%s_%08x", stem, hash.Sum32()))
}

func readAvatar(ctx context.Context, client *psftp.Client, sources []string) ([]byte, string) {
	for _, source := range sources {
		data, err := client.ReadFile(ctx, source, 20<<20)
		if err != nil {
			continue
		}
		switch http.DetectContentType(data) {
		case "image/png":
			return data, ".png"
		case "image/jpeg":
			return data, ".jpg"
		}
	}
	return nil, ""
}

func containsFold(values []string, wanted string) bool {
	for _, value := range values {
		if strings.EqualFold(value, wanted) {
			return true
		}
	}
	return false
}
