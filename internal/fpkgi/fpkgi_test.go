package fpkgi

import (
	"encoding/json"
	"testing"
)

func TestMarshalWritesFPKGiShape(t *testing.T) {
	data, err := Marshal([]Item{
		{URL: "http://192.168.1.20:8081/a.pkg", TitleID: "CUSA12345", Name: "Game", Region: "US", Version: "01.00", Size: 1000, CoverURL: "http://192.168.1.20:8081/c.png"},
		{URL: "", TitleID: "CUSA00000", Name: "No URL", Size: 1},
	})
	if err != nil {
		t.Fatal(err)
	}
	var document map[string]map[string]map[string]any
	if err := json.Unmarshal(data, &document); err != nil {
		t.Fatal(err)
	}
	entries := document["DATA"]
	if len(entries) != 1 {
		t.Fatalf("expected one entry, got %s", data)
	}
	entry := entries["http://192.168.1.20:8081/a.pkg"]
	if entry["title_id"] != "CUSA12345" || entry["name"] != "Game" || entry["region"] != "USA" || entry["version"] != "01.00" || entry["size"] != float64(1000) || entry["cover_url"] != "http://192.168.1.20:8081/c.png" {
		t.Fatalf("unexpected entry: %#v", entry)
	}
	for _, key := range []string{"release", "min_fw"} {
		if value, ok := entry[key]; !ok || value != nil {
			t.Fatalf("%s should be present and null, got %#v", key, entry)
		}
	}
}

func TestRegionMapsToFPKGiCodes(t *testing.T) {
	for input, want := range map[string]string{"US": "USA", "EP": "EUR", "jp": "JAP", "HP": "ASIA", "XX": "", "": ""} {
		if got := Region(input); got != want {
			t.Fatalf("Region(%q) = %q, want %q", input, got, want)
		}
	}
}
