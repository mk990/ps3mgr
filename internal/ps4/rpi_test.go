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

func TestRPIClientFreeSpaceParsesHexFigures(t *testing.T) {
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path != "/api/get_free_space" {
			return &http.Response{StatusCode: http.StatusNotFound, Body: io.NopCloser(bytes.NewBufferString("not found")), Header: make(http.Header)}, nil
		}
		// The installer reports byte counts as hex, which the RPI client
		// normalizes before decoding.
		body := `{ "status": "success", "path": "/user", "total": 0xE2D5B00000, "free": 0xB9B0F00000, "used": 0x290A400000 }`
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(bytes.NewBufferString(body)), Header: make(http.Header)}, nil
	})
	client := &RPIClient{Port: DefaultRPIPort, Client: &http.Client{Transport: transport}}
	capacity, err := client.FreeSpace(context.Background(), "192.168.1.4")
	if err != nil {
		t.Fatal(err)
	}
	if capacity.Total != 0xE2D5B00000 || capacity.Free != 0xB9B0F00000 || capacity.Used != 0x290A400000 {
		t.Fatalf("capacity = %+v", capacity)
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

func TestRPIClientPauseResumeAndFindTask(t *testing.T) {
	type request struct {
		path    string
		taskID  int
		content string
		subType int
	}
	var seen []request
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		var body struct {
			TaskID    int    `json:"task_id"`
			ContentID string `json:"content_id"`
			SubType   int    `json:"sub_type"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			return nil, err
		}
		seen = append(seen, request{path: r.URL.Path, taskID: body.TaskID, content: body.ContentID, subType: body.SubType})
		var response string
		switch r.URL.Path {
		case "/api/pause_task", "/api/resume_task":
			response = `{ "status": "success" }`
		case "/api/find_task":
			if body.ContentID == "UP0001-CUSA12345_00-ABCDEFGHIJKLMNOP" {
				response = `{ "status": "success", "task_id": 7 }`
			} else {
				// A lookup that matches nothing is reported by the console as a
				// background-download failure at HTTP 200, not as an empty result.
				response = `{ "status": "fail", "error_code": 0x80990015 }`
			}
		default:
			return &http.Response{StatusCode: http.StatusNotFound, Body: io.NopCloser(bytes.NewBufferString("not found")), Header: make(http.Header)}, nil
		}
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(bytes.NewBufferString(response)), Header: make(http.Header)}, nil
	})
	client := &RPIClient{Port: DefaultRPIPort, Client: &http.Client{Transport: transport}}

	if err := client.Pause(context.Background(), "192.168.1.4", 7); err != nil {
		t.Fatalf("pause: %v", err)
	}
	if err := client.Resume(context.Background(), "192.168.1.4", 7); err != nil {
		t.Fatalf("resume: %v", err)
	}
	taskID, found, err := client.FindTask(context.Background(), "192.168.1.4", "up0001-cusa12345_00-abcdefghijklmnop", TaskSubTypeDefault)
	if err != nil || !found || taskID != 7 {
		t.Fatalf("find task: task=%d found=%v err=%v", taskID, found, err)
	}
	taskID, found, err = client.FindTask(context.Background(), "192.168.1.4", "UP0001-CUSA99999_00-ABCDEFGHIJKLMNOP", TaskSubTypeDefault)
	if err != nil || found || taskID != 0 {
		t.Fatalf("missing task: task=%d found=%v err=%v", taskID, found, err)
	}

	want := []request{
		{path: "/api/pause_task", taskID: 7},
		{path: "/api/resume_task", taskID: 7},
		{path: "/api/find_task", content: "UP0001-CUSA12345_00-ABCDEFGHIJKLMNOP"},
		{path: "/api/find_task", content: "UP0001-CUSA99999_00-ABCDEFGHIJKLMNOP"},
	}
	if len(seen) != len(want) {
		t.Fatalf("requests = %+v, want %+v", seen, want)
	}
	for index, got := range seen {
		if got != want[index] {
			t.Fatalf("request %d = %+v, want %+v", index, got, want[index])
		}
	}
}

// The console truncates a content ID into a fixed size buffer, so an oversized
// or malformed value must be rejected before it can match an unrelated task.
func TestRPIClientFindTaskRejectsInvalidContentID(t *testing.T) {
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		t.Fatalf("unexpected request to %s", r.URL.Path)
		return nil, nil
	})
	client := &RPIClient{Port: DefaultRPIPort, Client: &http.Client{Transport: transport}}
	for _, contentID := range []string{"", "CUSA12345", "UP0001-CUSA12345_00-ABCDEFGHIJKLMNOPQ", `UP0001-CUSA12345_00-ABCDEFGHIJKLMN"P`} {
		if _, _, err := client.FindTask(context.Background(), "192.168.1.4", contentID, TaskSubTypeDefault); err == nil {
			t.Fatalf("content ID %q was accepted", contentID)
		}
	}
}

// A find_task lookup that fails for transport reasons must not be reported as
// "no task registered", which would hide a console that is unreachable.
func TestRPIClientFindTaskReportsTransportFailure(t *testing.T) {
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusInternalServerError, Body: io.NopCloser(bytes.NewBufferString("boom")), Header: make(http.Header)}, nil
	})
	client := &RPIClient{Port: DefaultRPIPort, Client: &http.Client{Transport: transport}}
	if _, found, err := client.FindTask(context.Background(), "192.168.1.4", "UP0001-CUSA12345_00-ABCDEFGHIJKLMNOP", TaskSubTypeDefault); err == nil || found {
		t.Fatalf("found=%v err=%v, want an error", found, err)
	}
}
