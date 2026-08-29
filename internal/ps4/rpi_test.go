package ps4

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"testing"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (fn roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) { return fn(request) }

func TestRPIClientOfficialProtocolAndHexProgress(t *testing.T) {
	var installURLs []string
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		var response string
		switch r.URL.Path {
		case "/api/is_exists":
			response = `{ "status": "success", "exists": "true", "size": 0x100 }`
		case "/api/install":
			var request struct {
				Type     string   `json:"type"`
				Packages []string `json:"packages"`
			}
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				return nil, err
			}
			if request.Type != "direct" {
				t.Errorf("unexpected type %q", request.Type)
			}
			installURLs = request.Packages
			response = `{ "status": "success", "task_id": 42, "title": "Test" }`
		case "/api/get_task_progress":
			response = `{ "status": "success", "bits": 0x0, "error": 0, "length": 0x20, "transferred": 0x20, "length_total": 0x100, "transferred_total": 0x100 }`
		default:
			return &http.Response{StatusCode: http.StatusNotFound, Body: io.NopCloser(bytes.NewBufferString("not found")), Header: make(http.Header)}, nil
		}
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(bytes.NewBufferString(response)), Header: make(http.Header)}, nil
	})
	client := &RPIClient{Port: DefaultRPIPort, Client: &http.Client{Transport: transport}}
	installed, err := client.IsInstalled(context.Background(), "192.168.1.4", "CUSA12345")
	if err != nil || !installed {
		t.Fatalf("is installed: %v, %v", installed, err)
	}
	taskID, err := client.Install(context.Background(), "192.168.1.4", []string{"http://host/part0.pkg", "http://host/part1.pkg"})
	if err != nil || taskID != 42 || len(installURLs) != 2 {
		t.Fatalf("install: task=%d urls=%v err=%v", taskID, installURLs, err)
	}
	want := []string{"http://host/part0.pkg", "http://host/part1.pkg"}
	for index, got := range installURLs {
		if got != want[index] {
			t.Fatalf("package URL = %q, want %q (RPI expects plain unescaped URLs)", got, want[index])
		}
	}
	progress, err := client.Progress(context.Background(), "192.168.1.4", taskID)
	if err != nil || progress.Transferred != 256 || progress.Total != 256 || !progress.Complete {
		t.Fatalf("progress: %+v err=%v", progress, err)
	}
}

// local_copy_percent reports 100 for the entire lifetime of a real RPI task,
// including immediately after registration and mid-transfer, so it must
// never be treated as a completion signal on its own.
func TestRPIClientProgressIgnoresLocalCopyPercent(t *testing.T) {
	responses := []string{
		`{ "status": "success", "bits": 0x0, "error": 0, "length": 0x0, "transferred": 0x0, "length_total": 0x0, "transferred_total": 0x0, "local_copy_percent": 100 }`,
		`{ "status": "success", "bits": 0x0, "error": 0, "length": 0x100, "transferred": 0x40, "length_total": 0x100, "transferred_total": 0x40, "local_copy_percent": 100 }`,
	}
	call := 0
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		response := responses[call]
		call++
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(bytes.NewBufferString(response)), Header: make(http.Header)}, nil
	})
	client := &RPIClient{Port: DefaultRPIPort, Client: &http.Client{Transport: transport}}
	for i := range responses {
		progress, err := client.Progress(context.Background(), "192.168.1.4", 1)
		if err != nil {
			t.Fatalf("progress %d: %v", i, err)
		}
		if progress.Complete {
			t.Fatalf("progress %d falsely reported complete: %+v", i, progress)
		}
	}
}
