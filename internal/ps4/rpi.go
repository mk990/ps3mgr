package ps4

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"
)

const DefaultRPIPort = 12800

var existsPattern = regexp.MustCompile(`(?i)"exists"\s*:\s*"?(true|false)"?`)
var hexJSONPattern = regexp.MustCompile(`0x[0-9A-Fa-f]+`)
var successPattern = regexp.MustCompile(`(?i)"status"\s*:\s*"success"`)
var failPattern = regexp.MustCompile(`(?i)"status"\s*:\s*"fail"`)

// Remote Package Installer copies a content ID into a fixed
// PKG_CONTENT_ID_SIZE buffer and silently truncates anything longer, which
// would then match the wrong task, so the length is enforced here. The pattern
// stays deliberately looser than contentIDPattern because fake PKGs built from
// PS2 titles carry non-CUSA content IDs.
var rpiContentIDPattern = regexp.MustCompile(`^[A-Z0-9_-]{36}$`)

// TaskSubTypeDefault matches tasks registered through /api/install, which
// always registers packages without a sub type.
const TaskSubTypeDefault = 0

type RPIClient struct {
	Port   int
	Client *http.Client
}

func NewRPIClient(port int, timeout time.Duration) *RPIClient {
	if port == 0 {
		port = DefaultRPIPort
	}
	if timeout <= 0 {
		timeout = 15 * time.Second
	}
	return &RPIClient{Port: port, Client: &http.Client{Timeout: timeout}}
}

// Detect verifies the official Remote Package Installer API rather than only
// accepting any process listening on port 12800.
func (c *RPIClient) Detect(ctx context.Context, ip string) (bool, int, error) {
	_, err := c.IsInstalled(ctx, ip, "CUSA00000")
	if err != nil {
		return false, 0, err
	}
	return true, 0, nil
}

func (c *RPIClient) Install(ctx context.Context, ip string, packageURLs []string) (int, error) {
	if len(packageURLs) == 0 {
		return 0, fmt.Errorf("package URL list is empty")
	}
	for index, packageURL := range packageURLs {
		parsed, err := url.Parse(packageURL)
		if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.User != nil || parsed.Fragment != "" {
			return 0, fmt.Errorf("invalid package URL at index %d", index)
		}
	}
	var response map[string]any
	if _, err := c.post(ctx, ip, "/api/install", map[string]any{"type": "direct", "packages": packageURLs}, &response); err != nil {
		return 0, err
	}
	taskID := int(number(response["task_id"]))
	if taskID <= 0 {
		return 0, fmt.Errorf("Remote Package Installer returned no task ID")
	}
	return taskID, nil
}

func (c *RPIClient) Progress(ctx context.Context, ip string, taskID int) (InstallProgress, error) {
	var response map[string]any
	if _, err := c.post(ctx, ip, "/api/get_task_progress", map[string]any{"task_id": taskID}, &response); err != nil {
		return InstallProgress{}, err
	}
	if result := firstNumber(response, "error"); result != 0 {
		return InstallProgress{}, fmt.Errorf("Remote Package Installer task failed with error %d", result)
	}
	transferred := firstNumber(response, "transferred_total", "transferred")
	total := firstNumber(response, "length_total", "length")
	current := firstString(response, "current_file", "title")
	// local_copy_percent reports 100 for the entire lifetime of a task
	// regardless of actual transfer progress, so it cannot be used as a
	// completion signal; transferred/total is the only reliable one.
	complete := total > 0 && transferred >= total
	return InstallProgress{Transferred: transferred, Total: total, CurrentFile: current, Complete: complete}, nil
}

func (c *RPIClient) IsInstalled(ctx context.Context, ip, titleID string) (bool, error) {
	if !titleIDPattern.MatchString(titleID) {
		return false, fmt.Errorf("invalid PS4 title ID %q", titleID)
	}
	body, err := c.post(ctx, ip, "/api/is_exists", map[string]string{"title_id": strings.ToUpper(titleID)}, nil)
	if err != nil {
		return false, err
	}
	match := existsPattern.FindSubmatch(body)
	if len(match) != 2 {
		return false, fmt.Errorf("Remote Package Installer returned an invalid existence response")
	}
	return strings.EqualFold(string(match[1]), "true"), nil
}

func (c *RPIClient) Cancel(ctx context.Context, ip string, taskID int) error {
	var first error
	if _, err := c.post(ctx, ip, "/api/stop_task", map[string]int{"task_id": taskID}, nil); err != nil {
		first = err
	}
	if _, err := c.post(ctx, ip, "/api/unregister_task", map[string]int{"task_id": taskID}, nil); err != nil && first == nil {
		first = err
	}
	return first
}

// Pause suspends a running task on the console. The task keeps its
// registration and its transferred bytes, so Resume continues it rather than
// restarting the download.
func (c *RPIClient) Pause(ctx context.Context, ip string, taskID int) error {
	_, err := c.post(ctx, ip, "/api/pause_task", map[string]int{"task_id": taskID}, nil)
	return err
}

// Resume continues a task previously suspended by Pause.
func (c *RPIClient) Resume(ctx context.Context, ip string, taskID int) error {
	_, err := c.post(ctx, ip, "/api/resume_task", map[string]int{"task_id": taskID}, nil)
	return err
}

// FindTask reports the task the console already has registered for contentID,
// so an install still running on the PS4 can be re-attached after a restart
// instead of being registered a second time. found is false when the console
// reports no matching task; subType selects the task kind and is
// TaskSubTypeDefault for packages queued through Install.
func (c *RPIClient) FindTask(ctx context.Context, ip, contentID string, subType int) (int, bool, error) {
	contentID = strings.ToUpper(strings.TrimSpace(contentID))
	if !rpiContentIDPattern.MatchString(contentID) {
		return 0, false, fmt.Errorf("invalid PS4 content ID %q", contentID)
	}
	var response map[string]any
	body, err := c.post(ctx, ip, "/api/find_task", map[string]any{"content_id": contentID, "sub_type": subType}, &response)
	if err != nil {
		// The console answers a lookup that matched nothing with a well-formed
		// failure body rather than an empty result, so that is reported as "no
		// task" instead of an error. Any other background-download failure is
		// reported the same way and is harmless here: the caller registers a
		// fresh task and that registration surfaces the real error.
		if failPattern.Match(body) {
			return 0, false, nil
		}
		return 0, false, err
	}
	taskID := int(number(response["task_id"]))
	if taskID <= 0 {
		return 0, false, nil
	}
	return taskID, true, nil
}

func (c *RPIClient) post(ctx context.Context, ip, path string, payload, decoded any) ([]byte, error) {
	data, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	endpoint := "http://" + net.JoinHostPort(ip, strconv.Itoa(c.Port)) + path
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := c.Client.Do(request)
	if err != nil {
		if errors.Is(err, io.EOF) {
			return nil, fmt.Errorf("Remote Package Installer at %s accepted the connection but sent no response; its installer service has likely crashed or hung on the console — relaunch it on the PS4 and retry: %w", endpoint, err)
		}
		if errors.Is(err, syscall.ECONNREFUSED) {
			return nil, fmt.Errorf("Remote Package Installer at %s refused the connection; confirm the installer service is running on the PS4 and the IP/port are correct: %w", endpoint, err)
		}
		return nil, fmt.Errorf("Remote Package Installer %s: %w", endpoint, err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, fmt.Errorf("Remote Package Installer returned HTTP %d: %s", response.StatusCode, strings.TrimSpace(string(body)))
	}
	if !successPattern.Match(body) {
		// The body is returned alongside the error so callers can inspect a
		// rejection the console reported at HTTP 200.
		return body, fmt.Errorf("Remote Package Installer rejected the request: %s", strings.TrimSpace(string(body)))
	}
	if decoded != nil {
		if err := json.Unmarshal(normalizeHexJSON(body), decoded); err != nil {
			return nil, fmt.Errorf("decode Remote Package Installer response: %w", err)
		}
	}
	return body, nil
}

func normalizeHexJSON(body []byte) []byte {
	return hexJSONPattern.ReplaceAllFunc(body, func(value []byte) []byte {
		number, err := strconv.ParseUint(string(value[2:]), 16, 64)
		if err != nil {
			return value
		}
		return []byte(strconv.FormatUint(number, 10))
	})
}

func firstNumber(value map[string]any, keys ...string) int64 {
	for _, key := range keys {
		if result := number(value[key]); result != 0 {
			return result
		}
	}
	return 0
}

func number(value any) int64 {
	switch value := value.(type) {
	case float64:
		return int64(value)
	case json.Number:
		result, _ := value.Int64()
		return result
	case string:
		result, _ := strconv.ParseInt(strings.TrimSpace(value), 0, 64)
		return result
	default:
		return 0
	}
}

func firstString(value map[string]any, keys ...string) string {
	for _, key := range keys {
		if text, ok := value[key].(string); ok && text != "" {
			return text
		}
	}
	return ""
}
