package ps4

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
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
	for index, encoded := range installURLs {
		if !strings.HasPrefix(encoded, "http%3A%2F%2F") {
			t.Fatalf("package URL was not encoded for RPI: %q", encoded)
		}
		decoded, decodeErr := url.QueryUnescape(encoded)
		want := []string{"http://host/part0.pkg", "http://host/part1.pkg"}[index]
		if decodeErr != nil || decoded != want {
			t.Fatalf("decoded package URL = %q, want %q (error %v)", decoded, want, decodeErr)
		}
	}
	progress, err := client.Progress(context.Background(), "192.168.1.4", taskID)
	if err != nil || progress.Transferred != 256 || progress.Total != 256 || !progress.Complete {
		t.Fatalf("progress: %+v err=%v", progress, err)
	}
}
