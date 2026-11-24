package ps5

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"ps3mgr/internal/domain"
	"ps3mgr/internal/events"
	"ps3mgr/internal/filelock"
)

func conversionFolder(t *testing.T) (string, string) {
	t.Helper()
	root := t.TempDir()
	source := filepath.Join(root, "Game v1.2")
	if err := os.MkdirAll(filepath.Join(source, "sce_sys"), 0o755); err != nil {
		t.Fatal(err)
	}
	for name, contents := range map[string]string{"sce_sys/param.json": `{"titleId":"PPSA00001","titleName":"Game"}`, "eboot.bin": "sample payload"} {
		if err := os.WriteFile(filepath.Join(source, name), []byte(contents), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root, source
}

func TestConvertFolderProtectsSourceAndExistingOutput(t *testing.T) {
	root, source := conversionFolder(t)
	service := Service{GameDir: root}
	ctx := context.Background()
	t.Setenv("PATH", t.TempDir())
	for _, tc := range []struct{ game, format, message string }{
		{"Game", "../outside", "choose exfat"},
		{"../outside", "exfat", "not found"},
		{"Game", "exfat", "requires MkPFS"},
	} {
		if _, err := service.ConvertFolder(ctx, tc.game, tc.format); err == nil || !strings.Contains(err.Error(), tc.message) {
			t.Fatalf("convert(%q, %q) = %v", tc.game, tc.format, err)
		}
	}
	if entries, err := os.ReadDir(root); err != nil || len(entries) != 1 {
		t.Fatalf("failed conversions left files behind: %v, %v", entries, err)
	}
	output := source + ".ffpkg"
	if err := os.WriteFile(output, []byte("original image"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := service.ConvertFolder(ctx, "PPSA00001", "FFPKG"); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("existing output: %v", err)
	}
	if data, err := os.ReadFile(output); err != nil || string(data) != "original image" {
		t.Fatalf("existing image changed: %q, %v", data, err)
	}
}

func TestConvertFolderWithInstalledBuilders(t *testing.T) {
	for format, builder := range map[string]string{"exfat": "mkpfs", "ffpkg": "makefs", "ffpfsc": "mkpfs"} {
		t.Run(format, func(t *testing.T) {
			if _, err := exec.LookPath(builder); err != nil {
				t.Skipf("%s not installed", builder)
			}
			root, source := conversionFolder(t)
			service := Service{GameDir: root}
			ctx := context.Background()
			name, err := service.ConvertFolder(ctx, "Game", format)
			if err != nil || name != filepath.Base(source)+"."+format {
				t.Fatalf("convert = %q, %v", name, err)
			}
			items, err := service.LocalGames(ctx, "")
			if err != nil || len(items) != 2 || items[0].LibraryID == items[1].LibraryID || items[0].ID != items[1].ID {
				t.Fatalf("source and converted edition = %+v, %v", items, err)
			}
			if data, err := os.ReadFile(filepath.Join(source, "eboot.bin")); err != nil || string(data) != "sample payload" {
				t.Fatalf("source changed: %q, %v", data, err)
			}
		})
	}
}

func TestConversionQueueRecoversInterruptedBuild(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test builder uses a shell script")
	}
	root, source := conversionFolder(t)
	bin := t.TempDir()
	t.Setenv("PATH", bin)
	outputScript := `#!/bin/sh
for output do :; done
printf 'test image' > "$output"
`
	builder := filepath.Join(bin, "mkpfs")
	if err := os.WriteFile(builder, []byte(outputScript+"while :; do /bin/sleep 0.05; done\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	bus := events.New()
	stream, unsubscribe := bus.Subscribe(16)
	defer unsubscribe()
	service := NewService(root, "", "", "", 0, 0, 0, 0, bus)
	defer service.Close(ctx)
	if err := service.RestoreConversions(); err != nil {
		t.Fatal(err)
	}
	jobs, err := service.EnqueueConversions(ctx, []string{"Game"}, "exfat")
	if err != nil {
		t.Fatal(err)
	}
	job := jobs[0]
	paths, err := service.conversionPaths(job.Game, job.OutputFormat)
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(paths.staged); err == nil {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal("builder did not create a staged image")
		case <-time.After(10 * time.Millisecond):
		}
	}
	if current, _ := service.Conversions.Get(job.ID); current.ConversionPhase != domain.ConversionBuilding || current.StartedAt == nil {
		t.Fatalf("running conversion: %+v", current)
	}
	if _, err := service.ConvertFolder(ctx, "Game", "exfat"); err == nil || !strings.Contains(err.Error(), "cannot lock conversion output") {
		t.Fatalf("concurrent conversion: %v", err)
	}
	journal := filepath.Join(root, ".ps3mgr-conversion-queue.json")
	beforeShutdown, err := os.ReadFile(journal)
	if err != nil {
		t.Fatal(err)
	}
	if err := service.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if current, _ := service.Conversions.Get(job.ID); current.State != domain.QueueFailed || !strings.Contains(current.Error, "interrupted") {
		t.Fatalf("shutdown conversion: %+v", current)
	}
	// Restore the on-disk state and partial image left by an unclean shutdown.
	if err := os.WriteFile(journal, beforeShutdown, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(paths.stage, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(paths.staged, []byte("partial"), 0o600); err != nil {
		t.Fatal(err)
	}
	lock, err := filelock.Open(paths.stage + ".lock")
	if err != nil {
		t.Fatal(err)
	}
	builderStillRunning := exec.CommandContext(ctx, "/bin/sh", "-c", "exec /bin/sleep 60")
	if err := filelock.Inherit(builderStillRunning, lock); err != nil {
		lock.Close()
		t.Fatal(err)
	}
	if err := builderStillRunning.Start(); err != nil {
		lock.Close()
		t.Fatal(err)
	}
	stopOrphan := func() {
		_ = builderStillRunning.Process.Kill()
		_ = builderStillRunning.Wait()
	}
	defer stopOrphan()
	if err := lock.Close(); err != nil {
		t.Fatal(err)
	}
	restarted := NewService(root, "", "", "", 0, 0, 0, 0, bus)
	defer restarted.Close(ctx)
	if err := restarted.RestoreConversions(); err != nil {
		t.Fatal(err)
	}
	if current, _ := restarted.Conversions.Get(job.ID); current.State != domain.QueueFailed || !strings.Contains(current.Error, "interrupted") {
		t.Fatalf("restored conversion: %+v", current)
	}
	if data, err := os.ReadFile(paths.staged); err != nil || string(data) != "partial" {
		t.Fatalf("locked stage changed: %q, %v", data, err)
	}
	stopOrphan()
	if err := os.WriteFile(builder, []byte(outputScript), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := restarted.RetryConversion(ctx, job.ID); err != nil {
		t.Fatal(err)
	}
	finalizing := false
	for {
		select {
		case <-ctx.Done():
			t.Fatal("conversion did not finish after retry")
		case event := <-stream:
			item, ok := event.Payload.(domain.Transfer)
			if !ok || item.ID != job.ID {
				continue
			}
			if event.Type == "ps5.conversion.queue.progress" && item.ConversionPhase == domain.ConversionFinalizing {
				finalizing = true
			}
			if event.Type != "ps5.conversion.queue.item_completed" {
				continue
			}
			if !finalizing || item.Attempts != 2 || item.Filename != filepath.Base(source)+".exfat" {
				t.Fatalf("completed conversion: %+v, finalizing event: %t", item, finalizing)
			}
			if data, err := os.ReadFile(paths.output); err != nil || string(data) != "test image" {
				t.Fatalf("rebuilt output: %q, %v", data, err)
			}
			if _, err := os.Lstat(paths.stage); !os.IsNotExist(err) {
				t.Fatalf("staging directory remained: %v", err)
			}
			return
		}
	}
}

func TestConversionPreflightRejectsUnwritableLibrary(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("test requires Unix directory permissions and an unprivileged user")
	}
	root, _ := conversionFolder(t)
	bin := t.TempDir()
	t.Setenv("PATH", bin)
	if err := os.WriteFile(filepath.Join(bin, "mkpfs"), nil, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(root, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(root, 0o755) })
	ctx := context.Background()
	service := NewService(root, "", "", "", 0, 0, 0, 0, nil)
	defer service.Close(ctx)
	if jobs, err := service.EnqueueConversions(ctx, []string{"Game"}, "exfat"); err == nil ||
		!strings.Contains(err.Error(), "mount the PS5 library read/write") || len(jobs) != 0 || len(service.Conversions.List()) != 0 {
		t.Fatalf("unwritable library: %+v, %v", jobs, err)
	}
}
