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
