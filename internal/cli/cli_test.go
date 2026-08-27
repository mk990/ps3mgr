package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestHelpAndUnknownCommandExitCodes(t *testing.T) {
	var output, errors bytes.Buffer
	runner := Runner{Out: &output, Err: &errors}
	if code := runner.Run(context.Background(), []string{"--help"}); code != 0 || !strings.Contains(output.String(), "local-games") {
		t.Fatalf("help code=%d output=%q", code, output.String())
	}
	if code := runner.Run(context.Background(), []string{"unknown"}); code != 2 {
		t.Fatalf("unknown command code=%d", code)
	}
}

func TestVersionCommand(t *testing.T) {
	previous := version
	version = "1.2.3-test"
	defer func() { version = previous }()

	var output bytes.Buffer
	code := (Runner{Out: &output, Err: &bytes.Buffer{}}).Run(context.Background(), []string{"version"})
	if code != 0 || output.String() != "ps3mgr 1.2.3-test\n" {
		t.Fatalf("code=%d output=%q", code, output.String())
	}
}

func TestLocalGamesFlagOverridesEnvironment(t *testing.T) {
	good := t.TempDir()
	if err := os.Mkdir(filepath.Join(good, "My Game"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PS3MGR_GAME_DIR", filepath.Join(good, "missing"))
	var output, errors bytes.Buffer
	code := (Runner{Out: &output, Err: &errors}).Run(context.Background(), []string{"local-games", "--dir", good, "--json"})
	if code != 0 || !strings.Contains(output.String(), "My Game") {
		t.Fatalf("code=%d output=%q errors=%q", code, output.String(), errors.String())
	}
}
