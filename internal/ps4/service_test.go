package ps4

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"reflect"
	"testing"

	"ps3mgr/internal/domain"
)

func TestOrderedPKGNamesPrioritizesPrimaryPatch(t *testing.T) {
	got := orderedPKGNames([]string{"readme.txt", "patch_b.pkg", "PATCH.PKG", "patch_a.PKG"})
	want := []string{"PATCH.PKG", "patch_a.PKG", "patch_b.pkg"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ordered packages = %#v, want %#v", got, want)
	}
}

func TestPatchDownloadNameUsesFlatNumberedFiles(t *testing.T) {
	base := "Game Title - CUSA12345"
	for index, want := range []string{
		"Game Title - CUSA12345.patch.pkg",
		"Game Title - CUSA12345.patch01.pkg",
		"Game Title - CUSA12345.patch02.pkg",
	} {
		if got := patchDownloadName(base, index); got != want {
			t.Fatalf("patch %d name = %q, want %q", index, got, want)
		}
	}
}

func TestDLCDownloadNameUsesFlatNumberedFiles(t *testing.T) {
	base := "Game Title - CUSA12345"
	for index, want := range []string{
		"Game Title - CUSA12345.DLC.pkg",
		"Game Title - CUSA12345.DLC01.pkg",
		"Game Title - CUSA12345.DLC02.pkg",
	} {
		if got := dlcDownloadName(base, index); got != want {
			t.Fatalf("DLC %d name = %q, want %q", index, got, want)
		}
	}
}

func TestSortForInstallPutsBaseGameBeforePatchAndDLC(t *testing.T) {
	packages := []Package{
		{ID: "a-dlc", TitleID: "CUSA00001", Format: "pkg-dlc"},
		{ID: "a-patch", TitleID: "CUSA00001", Format: "pkg-patch"},
		{ID: "a-game", TitleID: "CUSA00001", Format: "pkg-game"},
	}
	sortForInstall(packages)
	got := []string{packages[0].ID, packages[1].ID, packages[2].ID}
	want := []string{"a-game", "a-patch", "a-dlc"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("install order = %v, want %v", got, want)
	}
}

func TestSortForInstallKeepsTitlesGroupedInSelectionOrder(t *testing.T) {
	// Game B is selected before game A; each title stays together and keeps the
	// order it was first selected in, with the base ahead of its patch.
	packages := []Package{
		{ID: "b-patch", TitleID: "CUSA00002", Format: "pkg-patch"},
		{ID: "b-game", TitleID: "CUSA00002", Format: "pkg-game"},
		{ID: "a-game", TitleID: "CUSA00001", Format: "pkg-game"},
		{ID: "a-dlc", TitleID: "CUSA00001", Format: "pkg-dlc"},
	}
	sortForInstall(packages)
	got := []string{packages[0].ID, packages[1].ID, packages[2].ID, packages[3].ID}
	want := []string{"b-game", "b-patch", "a-game", "a-dlc"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("install order = %v, want %v", got, want)
	}
}

func TestSortForInstallPreservesOrderOfUnclassifiedPackages(t *testing.T) {
	// No title IDs: nothing can be grouped, so the selected order is preserved.
	packages := []Package{
		{ID: "first", Format: "pkg"},
		{ID: "second", Format: "pkg"},
	}
	sortForInstall(packages)
	if packages[0].ID != "first" || packages[1].ID != "second" {
		t.Fatalf("order = %v, want [first second]", []string{packages[0].ID, packages[1].ID})
	}
}

func TestPS4DownloadNameDoesNotRepeatBareTitleID(t *testing.T) {
	game := domain.Game{ID: "CUSA12345", Title: "cusa12345"}
	if got := ps4DownloadName(game); got != "CUSA12345" {
		t.Fatalf("download name = %q", got)
	}
}

func TestPS4DownloadNamePutsTitleBeforeTitleID(t *testing.T) {
	game := domain.Game{ID: "CUSA12345", Title: "Game/Title"}
	if got := ps4DownloadName(game); got != "Game-Title - CUSA12345" {
		t.Fatalf("download name = %q", got)
	}
}

func TestCompareMarksTitleInstalledForPatchesAndDLC(t *testing.T) {
	// The console has CUSA00001 but not CUSA00002. Remote Package Installer
	// answers per title, so the patch and DLC of an installed game must learn
	// that their title is present without claiming to be installed themselves.
	var queried []string
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path != "/api/is_exists" {
			return nil, fmt.Errorf("unexpected path %q", r.URL.Path)
		}
		var request struct {
			TitleID string `json:"title_id"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			return nil, err
		}
		queried = append(queried, request.TitleID)
		exists := "false"
		if request.TitleID == "CUSA00001" {
			exists = "true"
		}
		body := fmt.Sprintf(`{"status":"success","exists":"%s","size":0}`, exists)
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(bytes.NewBufferString(body)), Header: make(http.Header)}, nil
	})
	service := &Service{
		RPI:      &RPIClient{Port: DefaultRPIPort, Client: &http.Client{Transport: transport}},
		consoles: make(map[string]domain.Console),
		local: []Package{
			{ID: "a-game", TitleID: "CUSA00001", Format: "pkg-game"},
			{ID: "a-patch", TitleID: "CUSA00001", Format: "pkg-patch"},
			{ID: "a-dlc", TitleID: "CUSA00001", Format: "pkg-dlc"},
			{ID: "b-patch", TitleID: "CUSA00002", Format: "pkg-patch"},
			{ID: "loose", Format: "pkg"},
		},
	}
	items, err := service.Compare(context.Background(), "192.168.1.4")
	if err != nil {
		t.Fatalf("compare: %v", err)
	}
	want := map[string][2]bool{
		"a-game":  {true, true},
		"a-patch": {false, true},
		"a-dlc":   {false, true},
		"b-patch": {false, false},
		"loose":   {false, false},
	}
	for _, item := range items {
		if got := [2]bool{item.Installed, item.TitleInstalled}; got != want[item.ID] {
			t.Errorf("%s installed/title-installed = %v, want %v", item.ID, got, want[item.ID])
		}
	}
	// One request per distinct title ID, not one per package.
	if len(queried) != 2 {
		t.Fatalf("is_exists calls = %v, want one per title ID", queried)
	}
	if console, ok := service.Console("192.168.1.4"); !ok || console.GameCount != 1 {
		t.Fatalf("console game count = %d, want 1", console.GameCount)
	}
}

func TestSortForInstallLeadsWithABaseNamedByItsFileName(t *testing.T) {
	// God of War's base was recognised from its file name, so it leads the
	// whole batch; everything else keeps its title grouping and selected order.
	packages := []Package{
		{ID: "gow-dlc", TitleID: "CUSA07408", Format: "pkg-dlc"},
		{ID: "gow-base", TitleID: "CUSA07408", Format: "pkg-game", NamedBase: true},
		{ID: "bb-patch", TitleID: "CUSA00207", Format: "pkg-patch"},
		{ID: "bb-base", TitleID: "CUSA00207", Format: "pkg-game"},
	}
	sortForInstall(packages)
	got := []string{packages[0].ID, packages[1].ID, packages[2].ID, packages[3].ID}
	want := []string{"gow-base", "gow-dlc", "bb-base", "bb-patch"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("install order = %v, want %v", got, want)
	}
}

func TestSortForInstallKeepsNamedBasesInSelectedOrder(t *testing.T) {
	packages := []Package{
		{ID: "second-base", TitleID: "CUSA00002", Format: "pkg-game", NamedBase: true},
		{ID: "first-patch", TitleID: "CUSA00001", Format: "pkg-patch"},
		{ID: "first-base", TitleID: "CUSA00001", Format: "pkg-game", NamedBase: true},
	}
	sortForInstall(packages)
	got := []string{packages[0].ID, packages[1].ID, packages[2].ID}
	want := []string{"second-base", "first-base", "first-patch"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("install order = %v, want %v", got, want)
	}
}
