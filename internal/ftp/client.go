package ftp

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"path"
	"strconv"
	"strings"
	"sync"
	"time"
)

var ErrResumeUnsupported = errors.New("FTP server does not support upload resume")

type Client struct {
	conn    net.Conn
	reader  *bufio.Reader
	host    string
	timeout time.Duration
	mu      sync.Mutex
}

func Dial(ctx context.Context, host, user, password string, timeout time.Duration) (*Client, error) {
	address := host
	if _, _, err := net.SplitHostPort(host); err != nil {
		address = net.JoinHostPort(host, "21")
	}
	conn, err := (&net.Dialer{Timeout: timeout}).DialContext(ctx, "tcp", address)
	if err != nil {
		return nil, fmt.Errorf("dial FTP %s: %w", address, err)
	}
	hostOnly, _, _ := net.SplitHostPort(address)
	c := &Client{conn: conn, reader: bufio.NewReader(conn), host: hostOnly, timeout: timeout}
	if code, message, err := c.readResponse(ctx); err != nil || code != 220 {
		conn.Close()
		if err != nil {
			return nil, fmt.Errorf("read FTP greeting: %w", err)
		}
		return nil, fmt.Errorf("FTP greeting rejected: %d %s", code, message)
	}
	code, message, err := c.command(ctx, "USER "+cleanArg(user))
	if err != nil {
		conn.Close()
		return nil, err
	}
	if code == 331 {
		code, message, err = c.command(ctx, "PASS "+cleanArg(password))
	}
	if err != nil || (code != 230 && code != 202) {
		conn.Close()
		if err != nil {
			return nil, fmt.Errorf("FTP login: %w", err)
		}
		return nil, fmt.Errorf("FTP login rejected: %d %s", code, message)
	}
	_, _, _ = c.command(ctx, "TYPE I")
	return c, nil
}

func (c *Client) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	_ = c.writeLine(context.Background(), "QUIT")
	return c.conn.Close()
}

func (c *Client) Noop(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	code, message, err := c.command(ctx, "NOOP")
	if err != nil {
		return err
	}
	if code != 200 {
		return fmt.Errorf("FTP NOOP failed: %d %s", code, message)
	}
	return nil
}

func (c *Client) Names(ctx context.Context, remotePath string) ([]string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	var data bytes.Buffer
	if err := c.dataCommand(ctx, "NLST "+cleanPath(remotePath), &data, nil); err != nil {
		return nil, err
	}
	var names []string
	for _, line := range strings.Split(strings.ReplaceAll(data.String(), "\r", ""), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || line == "." || line == ".." {
			continue
		}
		names = append(names, path.Base(strings.TrimSuffix(line, "/")))
	}
	return names, nil
}

func (c *Client) ReadFile(ctx context.Context, remotePath string, limit int64) ([]byte, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	var data bytes.Buffer
	writer := io.Writer(&data)
	if limit > 0 {
		writer = &limitWriter{writer: &data, remaining: limit}
	}
	if err := c.dataCommand(ctx, "RETR "+cleanPath(remotePath), writer, nil); err != nil {
		return nil, err
	}
	return data.Bytes(), nil
}

func (c *Client) Size(ctx context.Context, remotePath string) (int64, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	code, message, err := c.command(ctx, "SIZE "+cleanPath(remotePath))
	if err != nil {
		return 0, err
	}
	if code != 213 {
		return 0, fmt.Errorf("remote file size unavailable: %d %s", code, message)
	}
	value, err := strconv.ParseInt(strings.TrimSpace(message), 10, 64)
	if err != nil {
		return 0, fmt.Errorf("parse remote file size: %w", err)
	}
	return value, nil
}

func (c *Client) MakeDirAll(ctx context.Context, remotePath string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	remotePath = path.Clean("/" + strings.TrimPrefix(cleanPath(remotePath), "/"))
	current := ""
	for _, segment := range strings.Split(strings.TrimPrefix(remotePath, "/"), "/") {
		if segment == "" {
			continue
		}
		current += "/" + segment
		code, message, err := c.command(ctx, "MKD "+current)
		if err != nil {
			return err
		}
		if code != 257 && code != 250 && code != 550 {
			return fmt.Errorf("create remote directory %s: %d %s", current, code, message)
		}
	}
	return nil
}

func (c *Client) Store(ctx context.Context, remotePath string, source io.Reader, offset int64, progress func(int64)) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if offset > 0 {
		code, message, err := c.command(ctx, fmt.Sprintf("REST %d", offset))
		if err != nil {
			return err
		}
		if code != 350 {
			return fmt.Errorf("%w: %d %s", ErrResumeUnsupported, code, message)
		}
	}
	return c.dataCommand(ctx, "STOR "+cleanPath(remotePath), nil, func(conn net.Conn) error {
		buffer := make([]byte, 256*1024)
		for {
			if err := ctx.Err(); err != nil {
				return err
			}
			n, readErr := source.Read(buffer)
			if n > 0 {
				if err := writeAll(conn, buffer[:n]); err != nil {
					return err
				}
				if progress != nil {
					progress(int64(n))
				}
			}
			if readErr != nil {
				if errors.Is(readErr, io.EOF) {
					return nil
				}
				return readErr
			}
		}
	})
}

// Retrieve copies a remote file to destination and reports bytes received.
func (c *Client) Retrieve(ctx context.Context, remotePath string, destination io.Writer, progress func(int64)) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	dataConn, err := c.openPassive(ctx)
	if err != nil {
		return err
	}
	defer dataConn.Close()
	if err := c.writeLine(ctx, "RETR "+cleanPath(remotePath)); err != nil {
		return err
	}
	code, message, err := c.readResponse(ctx)
	if err != nil {
		return err
	}
	if code != 125 && code != 150 {
		return fmt.Errorf("FTP RETR failed: %d %s", code, message)
	}
	buffer := make([]byte, 256*1024)
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		n, readErr := dataConn.Read(buffer)
		if n > 0 {
			if _, err := destination.Write(buffer[:n]); err != nil {
				return err
			}
			if progress != nil {
				progress(int64(n))
			}
		}
		if readErr != nil {
			if errors.Is(readErr, io.EOF) {
				break
			}
			return readErr
		}
	}
	if err := dataConn.Close(); err != nil {
		return err
	}
	code, message, err = c.readResponse(ctx)
	if err != nil {
		return err
	}
	if code != 226 && code != 250 {
		return fmt.Errorf("FTP transfer incomplete: %d %s", code, message)
	}
	return nil
}

func (c *Client) dataCommand(ctx context.Context, command string, destination io.Writer, upload func(net.Conn) error) error {
	dataConn, err := c.openPassive(ctx)
	if err != nil {
		return err
	}
	defer dataConn.Close()
	if err := c.writeLine(ctx, command); err != nil {
		return err
	}
	code, message, err := c.readResponse(ctx)
	if err != nil {
		return err
	}
	if code != 125 && code != 150 {
		return fmt.Errorf("FTP %s failed: %d %s", strings.Fields(command)[0], code, message)
	}
	if upload != nil {
		err = upload(dataConn)
		if closeErr := dataConn.Close(); err == nil {
			err = closeErr
		}
	} else if destination != nil {
		_, err = copyContext(ctx, destination, dataConn)
		_ = dataConn.Close()
	}
	if err != nil {
		return err
	}
	code, message, err = c.readResponse(ctx)
	if err != nil {
		return err
	}
	if code != 226 && code != 250 {
		return fmt.Errorf("FTP transfer incomplete: %d %s", code, message)
	}
	return nil
}

func (c *Client) openPassive(ctx context.Context) (net.Conn, error) {
	code, message, err := c.command(ctx, "EPSV")
	var address string
	if err == nil && code == 229 {
		parts := strings.Split(message, "|")
		if len(parts) >= 4 {
			port := parts[len(parts)-2]
			if _, parseErr := strconv.Atoi(port); parseErr == nil {
				address = net.JoinHostPort(c.host, port)
			}
		}
	}
	if address == "" {
		code, message, err = c.command(ctx, "PASV")
		if err != nil || code != 227 {
			return nil, fmt.Errorf("enter FTP passive mode: %d %s: %w", code, message, err)
		}
		start, end := strings.Index(message, "("), strings.Index(message, ")")
		if start < 0 || end <= start {
			return nil, fmt.Errorf("invalid PASV response: %s", message)
		}
		fields := strings.Split(message[start+1:end], ",")
		if len(fields) != 6 {
			return nil, fmt.Errorf("invalid PASV address: %s", message)
		}
		p1, e1 := strconv.Atoi(strings.TrimSpace(fields[4]))
		p2, e2 := strconv.Atoi(strings.TrimSpace(fields[5]))
		if e1 != nil || e2 != nil {
			return nil, fmt.Errorf("invalid PASV port: %s", message)
		}
		address = net.JoinHostPort(c.host, strconv.Itoa(p1*256+p2))
	}
	return (&net.Dialer{Timeout: c.timeout}).DialContext(ctx, "tcp", address)
}

func (c *Client) command(ctx context.Context, command string) (int, string, error) {
	if err := c.writeLine(ctx, command); err != nil {
		return 0, "", err
	}
	return c.readResponse(ctx)
}

func (c *Client) writeLine(ctx context.Context, command string) error {
	if err := setDeadline(ctx, c.conn, c.timeout); err != nil {
		return err
	}
	_, err := io.WriteString(c.conn, command+"\r\n")
	return err
}

func (c *Client) readResponse(ctx context.Context) (int, string, error) {
	if err := setDeadline(ctx, c.conn, c.timeout); err != nil {
		return 0, "", err
	}
	line, err := c.reader.ReadString('\n')
	if err != nil {
		return 0, "", err
	}
	line = strings.TrimRight(line, "\r\n")
	if len(line) < 3 {
		return 0, "", fmt.Errorf("short FTP response %q", line)
	}
	code, err := strconv.Atoi(line[:3])
	if err != nil {
		return 0, "", fmt.Errorf("invalid FTP response %q", line)
	}
	message := strings.TrimSpace(line[3:])
	if len(line) > 3 && line[3] == '-' {
		prefix := line[:3] + " "
		var lines []string
		lines = append(lines, strings.TrimSpace(line[4:]))
		for {
			line, err = c.reader.ReadString('\n')
			if err != nil {
				return 0, "", err
			}
			line = strings.TrimRight(line, "\r\n")
			if strings.HasPrefix(line, prefix) {
				lines = append(lines, strings.TrimSpace(line[4:]))
				break
			}
			lines = append(lines, line)
		}
		message = strings.Join(lines, "\n")
	}
	return code, message, nil
}

func cleanArg(value string) string { return strings.NewReplacer("\r", "", "\n", "").Replace(value) }

func cleanPath(value string) string {
	value = cleanArg(value)
	if value == "" {
		return "/"
	}
	return path.Clean(value)
}

func setDeadline(ctx context.Context, conn net.Conn, fallback time.Duration) error {
	deadline := time.Now().Add(fallback)
	if value, ok := ctx.Deadline(); ok && value.Before(deadline) {
		deadline = value
	}
	return conn.SetDeadline(deadline)
}

func copyContext(ctx context.Context, destination io.Writer, source io.Reader) (int64, error) {
	var total int64
	buffer := make([]byte, 128*1024)
	for {
		if err := ctx.Err(); err != nil {
			return total, err
		}
		n, readErr := source.Read(buffer)
		if n > 0 {
			written, writeErr := destination.Write(buffer[:n])
			total += int64(written)
			if writeErr != nil {
				return total, writeErr
			}
		}
		if readErr != nil {
			if errors.Is(readErr, io.EOF) {
				return total, nil
			}
			return total, readErr
		}
	}
}

func writeAll(writer io.Writer, data []byte) error {
	for len(data) > 0 {
		n, err := writer.Write(data)
		if err != nil {
			return err
		}
		data = data[n:]
	}
	return nil
}

type limitWriter struct {
	writer    io.Writer
	remaining int64
}

func (w *limitWriter) Write(data []byte) (int, error) {
	if int64(len(data)) > w.remaining {
		data = data[:w.remaining]
	}
	if len(data) == 0 {
		return 0, fmt.Errorf("FTP response exceeds size limit")
	}
	n, err := w.writer.Write(data)
	w.remaining -= int64(n)
	return n, err
}
