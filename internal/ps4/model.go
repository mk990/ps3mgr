package ps4

import "time"

const Platform = "ps4"

type PackagePart struct {
	Name string `json:"name"`
	Size int64  `json:"size"`
	Path string `json:"-"`
}

// Package is one installable PS4 package. Split PKGs are represented as one
// package with their parts in the order required by Remote Package Installer.
type Package struct {
	ID        string        `json:"id"`
	ContentID string        `json:"content_id,omitempty"`
	TitleID   string        `json:"title_id,omitempty"`
	Title     string        `json:"title"`
	Format    string        `json:"format"`
	Version   string        `json:"version,omitempty"`
	Region    string        `json:"region,omitempty"`
	Size      int64         `json:"size"`
	Parts     []PackagePart `json:"parts"`
	CoverPath string        `json:"-"`
	CoverURL  string        `json:"cover_url,omitempty"`
	Installed bool          `json:"installed"`
}

type JobState string

const (
	StateWaiting           JobState = "WAITING"
	StateValidating        JobState = "VALIDATING"
	StateServing           JobState = "SERVING"
	StateRequestingInstall JobState = "REQUESTING_INSTALL"
	StateDownloading       JobState = "DOWNLOADING"
	StateVerifying         JobState = "VERIFYING"
	StateCompleted         JobState = "COMPLETED"
	StateFailed            JobState = "FAILED"
	StateCancelled         JobState = "CANCELLED"
)

type Job struct {
	ID               string     `json:"id"`
	QueueID          string     `json:"queue_id"`
	Platform         string     `json:"platform"`
	ConsoleIP        string     `json:"console_ip"`
	Package          Package    `json:"package"`
	State            JobState   `json:"state"`
	TaskID           int        `json:"task_id,omitempty"`
	CurrentFile      string     `json:"current_file,omitempty"`
	BytesTransferred int64      `json:"bytes_transferred"`
	TotalBytes       int64      `json:"total_bytes"`
	Percentage       float64    `json:"percentage"`
	Speed            int64      `json:"speed"`
	ETASeconds       int64      `json:"eta_seconds"`
	Error            string     `json:"error,omitempty"`
	Attempts         int        `json:"attempts"`
	CreatedAt        time.Time  `json:"created_at"`
	StartedAt        *time.Time `json:"started_at,omitempty"`
	FinishedAt       *time.Time `json:"finished_at,omitempty"`
}

type InstallProgress struct {
	Transferred int64
	Total       int64
	CurrentFile string
	Complete    bool
}

type Publisher interface {
	Publish(eventType string, payload any)
}
