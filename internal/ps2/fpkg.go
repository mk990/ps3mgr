package ps2

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"ps3mgr/internal/orbis"
)

// fpkgPasscode is the all-zero passcode every fake PKG tool uses.
const fpkgPasscode = "00000000000000000000000000000000"

// FPKGConfig points the converter at the emulator template and the output
// directory. The conversion itself is implemented in internal/orbis, so no
// external tools or Wine are involved.
type FPKGConfig struct {
	// Emulator is the path to the PS2 emulator PKG template (JakV2.pkg).
	Emulator string
	// OutputDir receives the finished PS4 packages.
	OutputDir string
	// SupportDir optionally holds per game configs, LUA patches and backgrounds.
	SupportDir string
}

type FPKGStatus struct {
	Ready     bool              `json:"ready"`
	Emulator  string            `json:"emulator"`
	OutputDir string            `json:"output_dir"`
	Converter string            `json:"converter"`
	Checks    map[string]string `json:"checks"`
	Message   string            `json:"message,omitempty"`
}

type FPKGState string

const (
	FPKGWaiting    FPKGState = "WAITING"
	FPKGExtracting FPKGState = "EXTRACTING"
	FPKGImporting  FPKGState = "IMPORTING"
	FPKGPatching   FPKGState = "PATCHING"
	FPKGBuilding   FPKGState = "BUILDING"
	FPKGVerifying  FPKGState = "VERIFYING"
	FPKGCompleted  FPKGState = "COMPLETED"
	FPKGFailed     FPKGState = "FAILED"
	FPKGCancelled  FPKGState = "CANCELLED"
)

type FPKGProgress struct {
	Stage       FPKGState `json:"stage"`
	CurrentFile string    `json:"current_file,omitempty"`
	Percentage  float64   `json:"percentage"`
}

type FPKGJob struct {
	ID         string       `json:"id"`
	Game       Game         `json:"game"`
	State      FPKGState    `json:"state"`
	Progress   FPKGProgress `json:"progress"`
	OutputPath string       `json:"output_path,omitempty"`
	Error      string       `json:"error,omitempty"`
	Attempts   int          `json:"attempts"`
	CreatedAt  time.Time    `json:"created_at"`
	StartedAt  *time.Time   `json:"started_at,omitempty"`
	FinishedAt *time.Time   `json:"finished_at,omitempty"`
}

type FPKGBuilder struct {
	Config FPKGConfig
}

func (b FPKGBuilder) Status() FPKGStatus {
	status := FPKGStatus{
		Emulator:  b.Config.Emulator,
		OutputDir: b.Config.OutputDir,
		Converter: "built-in",
		Checks:    map[string]string{"converter": "built-in PS4 PKG writer"},
	}
	problems := make([]string, 0)
	if info, err := os.Stat(b.Config.Emulator); err != nil || info.IsDir() {
		message := fmt.Sprintf("emulator FPKG is unavailable: %s", b.Config.Emulator)
		status.Checks["emulator"] = message
		problems = append(problems, message)
	} else if err := verifyPS4PKG(b.Config.Emulator); err != nil {
		message := fmt.Sprintf("emulator FPKG is invalid: %v", err)
		status.Checks["emulator"] = message
		problems = append(problems, message)
	} else {
		status.Checks["emulator"] = b.Config.Emulator
	}
	if info, err := os.Stat(b.Config.OutputDir); err != nil || !info.IsDir() {
		message := fmt.Sprintf("PS4 output directory is unavailable: %s", b.Config.OutputDir)
		status.Checks["output"] = message
		problems = append(problems, message)
	} else {
		status.Checks["output"] = b.Config.OutputDir
	}
	status.Ready = len(problems) == 0
	status.Message = strings.Join(problems, "; ")
	return status
}

// Build converts a PS2 ISO into an installable PS4 package. The ISO is streamed
// straight into the package, so no second copy of it is written to disk.
func (b FPKGBuilder) Build(ctx context.Context, game Game, progress func(FPKGProgress)) (output string, err error) {
	if ctx == nil {
		ctx = context.Background()
	}
	serial, err := fpkgSerial(game)
	if err != nil {
		return "", err
	}
	if status := b.Status(); !status.Ready {
		return "", fmt.Errorf("PS2 FPKG converter is not ready: %s", status.Message)
	}
	work, err := os.MkdirTemp(b.Config.OutputDir, ".ps2-fpkg-")
	if err != nil {
		return "", fmt.Errorf("create FPKG workspace: %w", err)
	}
	defer os.RemoveAll(work)
	output = filepath.Join(b.Config.OutputDir, safeFPKGName(game.Title)+"_"+serial+".pkg")
	if _, statErr := os.Lstat(output); statErr == nil {
		return "", fmt.Errorf("refusing to overwrite existing PS4 package: %s", output)
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return "", fmt.Errorf("check PS4 package destination: %w", statErr)
	}
	report := func(stage FPKGState, percent float64, current string) {
		if progress != nil {
			progress(FPKGProgress{Stage: stage, Percentage: percent, CurrentFile: current})
		}
	}

	report(FPKGExtracting, 5, filepath.Base(b.Config.Emulator))
	template, err := orbis.OpenPackage(b.Config.Emulator, fpkgPasscode)
	if err != nil {
		return "", fmt.Errorf("open emulator FPKG: %w", err)
	}
	emulatorDir := filepath.Join(work, "image0")
	if err = os.MkdirAll(emulatorDir, 0o755); err != nil {
		template.Close()
		return "", err
	}
	if err = template.ExtractFiles(emulatorDir); err != nil {
		template.Close()
		return "", fmt.Errorf("extract emulator FPKG: %w", err)
	}
	sfo, err := template.ParamSFO()
	if err != nil {
		template.Close()
		return "", fmt.Errorf("read emulator param.sfo: %w", err)
	}
	contentID := patchContentID(template.Info().ContentID, serial)
	template.Close()
	if err = ctx.Err(); err != nil {
		return "", err
	}

	tree, err := orbis.DirTree(emulatorDir)
	if err != nil {
		return "", fmt.Errorf("read extracted emulator: %w", err)
	}
	tree.WithContext(ctx)

	report(FPKGImporting, 25, game.ISOFilename)
	if err = tree.AddFile("image/disc01.iso", game.ISOPath); err != nil {
		return "", fmt.Errorf("import PS2 ISO: %w", err)
	}

	report(FPKGPatching, 40, "config-emu-ps4.txt")
	if err = b.installSupportFiles(tree, game, serial); err != nil {
		return "", err
	}
	if err = patchEmulatorSFO(sfo, contentID, serial, game.Title); err != nil {
		return "", err
	}
	tree.AddData("sce_sys/param.sfo", sfo.Serialize())
	if err = b.installArtwork(tree, game, serial); err != nil {
		return "", err
	}

	report(FPKGBuilding, 55, filepath.Base(output))
	built := filepath.Join(work, "output.pkg")
	buildProgress := func(stage string, percent float64) {
		// The package writer reports 0-100 over the slowest part of the job.
		report(FPKGBuilding, 55+percent*0.4, stage)
	}
	if err = orbis.Build(orbis.BuildOptions{
		ContentID:    contentID,
		Passcode:     fpkgPasscode,
		Root:         tree,
		Timestamp:    time.Now().UTC(),
		CreationDate: time.Now().UTC(),
		Context:      ctx,
	}, built, buildProgress); err != nil {
		return "", fmt.Errorf("build PS4 package: %w", err)
	}

	report(FPKGVerifying, 98, filepath.Base(output))
	if err = verifyPS4PKG(built); err != nil {
		return "", err
	}
	if err = publishFile(built, output); err != nil {
		return "", fmt.Errorf("publish PS4 package: %w", err)
	}
	report(FPKGCompleted, 100, filepath.Base(output))
	return output, nil
}

// fpkgSerial normalises a PS2 serial such as SLUS-20946 into SLUS20946.
func fpkgSerial(game Game) (string, error) {
	if game.ID == "" || game.ID == "unknown" {
		return "", fmt.Errorf("PS2 game %q needs a known serial before FPKG conversion", game.Title)
	}
	serial := strings.ToUpper(strings.ReplaceAll(game.ID, "-", ""))
	if len(serial) != 9 {
		return "", fmt.Errorf("unsupported PS2 serial for FPKG: %s", game.ID)
	}
	return serial, nil
}

// patchContentID swaps the title ID inside the template content ID.
func patchContentID(contentID, serial string) string {
	if len(contentID) != 36 {
		return contentID
	}
	return contentID[:7] + serial + contentID[16:]
}

// supportFile finds an optional per game file, accepting both the dashed and
// the compact spelling of the serial.
func (b FPKGBuilder) supportFile(kind, name string, game Game, serial string) string {
	if b.Config.SupportDir == "" {
		return ""
	}
	root := filepath.Join(b.Config.SupportDir, "fpkg", kind)
	for _, key := range []string{strings.ToUpper(game.ID), serial} {
		if key == "" {
			continue
		}
		path := filepath.Join(root, strings.ReplaceAll(name, "{serial}", key))
		if info, err := os.Stat(path); err == nil && !info.IsDir() {
			return path
		}
	}
	return ""
}

// installSupportFiles applies the emulator config and optional per game patches.
func (b FPKGBuilder) installSupportFiles(tree *orbis.Tree, game Game, serial string) error {
	if custom := b.supportFile("configs", "{serial}.txt", game, serial); custom != "" {
		if err := tree.AddFile("config-emu-ps4.txt", custom); err != nil {
			return fmt.Errorf("install custom emulator config: %w", err)
		}
	}
	data, err := tree.ReadFile("config-emu-ps4.txt")
	if err != nil {
		return fmt.Errorf("read emulator config: %w", err)
	}
	lines := strings.Split(strings.ReplaceAll(string(data), "\r\n", "\n"), "\n")
	found := false
	for i, line := range lines {
		if strings.HasPrefix(strings.TrimSpace(line), "--max-disc-num=") {
			lines[i] = "--max-disc-num=1"
			found = true
		}
	}
	if !found {
		lines = append(lines, "--max-disc-num=1")
	}
	tree.AddData("config-emu-ps4.txt", []byte(strings.Join(lines, "\n")))

	if lua := b.supportFile("lua", "{serial}_config.lua", game, serial); lua != "" {
		if err := tree.AddFile("patches/"+filepath.Base(lua), lua); err != nil {
			return fmt.Errorf("install custom LUA: %w", err)
		}
	}
	return nil
}

// installArtwork renders the PS4 icons from the local cover art.
func (b FPKGBuilder) installArtwork(tree *orbis.Tree, game Game, serial string) error {
	if game.CoverPath == "" {
		return nil
	}
	icon, err := orbis.ResizePNG(game.CoverPath, 512, 512)
	if err != nil {
		return fmt.Errorf("render PS4 icon: %w", err)
	}
	tree.AddData("sce_sys/icon0.png", icon)
	save, err := orbis.ResizePNG(game.CoverPath, 228, 128)
	if err != nil {
		return fmt.Errorf("render PS4 save icon: %w", err)
	}
	tree.AddData("sce_sys/save_data.png", save)
	background := b.background(game, serial)
	if background == "" {
		return nil
	}
	picture, err := orbis.ResizePNG(background, 1920, 1080)
	if err != nil {
		return fmt.Errorf("render PS4 background: %w", err)
	}
	tree.AddData("sce_sys/pic1.png", picture)
	return nil
}

// patchEmulatorSFO rewrites the template metadata for the converted game.
func patchEmulatorSFO(sfo *orbis.ParamSFO, contentID, serial, title string) error {
	if err := sfo.SetText("CONTENT_ID", contentID); err != nil {
		return err
	}
	if err := sfo.SetText("TITLE_ID", serial); err != nil {
		return err
	}
	if err := sfo.SetText("TITLE", strings.TrimSpace(title)); err != nil {
		return fmt.Errorf("PS2 title does not fit the emulator template: %w", err)
	}
	return nil
}

// background returns the configured 1920x1080 source image, if any.
func (b FPKGBuilder) background(game Game, serial string) string {
	if b.Config.SupportDir == "" {
		return ""
	}
	root := filepath.Join(b.Config.SupportDir, "fpkg", "backgrounds")
	for _, key := range []string{strings.ToUpper(game.ID), serial} {
		if path := firstImage(root, key); path != "" {
			return path
		}
	}
	return ""
}

func verifyPS4PKG(path string) error {
	file, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("PKG is missing: %w", err)
	}
	defer file.Close()
	header := make([]byte, 4)
	if _, err := io.ReadFull(file, header); err != nil {
		return fmt.Errorf("PKG is truncated: %w", err)
	}
	if binary.BigEndian.Uint32(header) != 0x7f434e54 {
		return fmt.Errorf("file does not have PS4 PKG magic")
	}
	return nil
}

func publishFile(source, destination string) error {
	if err := os.Rename(source, destination); err == nil {
		return nil
	}
	if err := os.Link(source, destination); err == nil {
		return nil
	}
	in, err := os.Open(source)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(destination, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(out, in)
	closeErr := out.Close()
	if copyErr != nil || closeErr != nil {
		_ = os.Remove(destination)
		if copyErr != nil {
			return copyErr
		}
		return closeErr
	}
	return nil
}

func firstImage(root, base string) string {
	for _, extension := range []string{".png", ".jpg", ".jpeg", ".webp", ".PNG", ".JPG", ".JPEG", ".WEBP"} {
		path := filepath.Join(root, base+extension)
		if info, err := os.Stat(path); err == nil && !info.IsDir() {
			return path
		}
	}
	return ""
}

func safeFPKGName(value string) string {
	value = strings.TrimSpace(value)
	value = strings.Map(func(r rune) rune {
		switch r {
		case '/', '\\', ':', '*', '?', '"', '<', '>', '|':
			return '_'
		default:
			if r < 32 {
				return -1
			}
			return r
		}
	}, value)
	if value == "" || value == "." || value == ".." {
		return "PS2_Game"
	}
	return value
}
