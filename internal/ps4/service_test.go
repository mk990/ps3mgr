package ps4

import (
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
