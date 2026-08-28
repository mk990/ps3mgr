package ps2

import "time"

const Platform = "ps2"

type Game struct {
	ID           string `json:"id"`
	PublicID     string `json:"public_id"`
	Title        string `json:"title"`
	ISOPath      string `json:"-"`
	ISOFilename  string `json:"iso_filename"`
	Size         int64  `json:"size"`
	CoverPath    string `json:"-"`
	CoverURL     string `json:"cover_url,omitempty"`
	OPLReady     bool   `json:"opl_ready"`
	USBInstalled bool   `json:"usb_installed"`
	QueueStatus  State  `json:"queue_status,omitempty"`
}

type USBTarget struct {
	ID                string `json:"id"`
	Name              string `json:"name"`
	MountPath         string `json:"mount_path"`
	Filesystem        string `json:"filesystem"`
	FAT32Compatible   bool   `json:"fat32_compatible"`
	FAT32Status       string `json:"fat32_status"`
	CompatibilityNote string `json:"compatibility_note"`
	TotalBytes        int64  `json:"total_bytes"`
	UsedBytes         int64  `json:"used_bytes"`
	FreeBytes         int64  `json:"free_bytes"`
	ReadOnly          bool   `json:"read_only"`
	Available         bool   `json:"available"`
	OPLReady          bool   `json:"opl_ready"`
}

type USBScanIssue struct {
	Path   string `json:"path"`
	Reason string `json:"reason"`
}

type USBDiscovery struct {
	Root    string         `json:"root"`
	Mode    string         `json:"mode"`
	Targets []USBTarget    `json:"targets"`
	Issues  []USBScanIssue `json:"issues"`
}

type State string

const (
	StateWaiting    State = "WAITING"
	StatePreparing  State = "PREPARING"
	StateConverting State = "CONVERTING"
	StateWriting    State = "WRITING"
	StateVerifying  State = "VERIFYING"
	StateCompleted  State = "COMPLETED"
	StateFailed     State = "FAILED"
	StateCancelled  State = "CANCELLED"
	StatePaused     State = "PAUSED"
)

type Progress struct {
	Stage       State   `json:"stage"`
	CurrentFile string  `json:"current_file,omitempty"`
	Bytes       int64   `json:"bytes"`
	Total       int64   `json:"total"`
	Percentage  float64 `json:"percentage"`
	Speed       int64   `json:"speed"`
	ETASeconds  int64   `json:"eta_seconds"`
}

type Job struct {
	ID          string     `json:"id"`
	QueueID     string     `json:"queue_id"`
	Platform    string     `json:"platform"`
	Game        Game       `json:"game"`
	USBID       string     `json:"usb_id"`
	State       State      `json:"state"`
	Progress    Progress   `json:"progress"`
	Error       string     `json:"error,omitempty"`
	Recoverable bool       `json:"recoverable"`
	Attempts    int        `json:"attempts"`
	CreatedAt   time.Time  `json:"created_at"`
	StartedAt   *time.Time `json:"started_at,omitempty"`
	FinishedAt  *time.Time `json:"finished_at,omitempty"`
}

type OPLResult struct {
	Strategy      string           `json:"strategy"`
	Root          string           `json:"root"`
	Files         []string         `json:"files"`
	ExpectedSizes map[string]int64 `json:"expected_sizes"`
	ConfigFile    string           `json:"config_file,omitempty"`
	Bytes         int64            `json:"bytes"`
}

type CompareResult struct {
	Game      Game `json:"game"`
	Installed bool `json:"installed"`
}
