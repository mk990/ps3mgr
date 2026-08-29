package domain

import "time"

type GameState string

const (
	StateNotInstalled GameState = "NOT_INSTALLED"
	StateInstalled    GameState = "INSTALLED"
	StateQueued       GameState = "QUEUED"
	StateTransferring GameState = "TRANSFERRING"
	StateCompleted    GameState = "COMPLETED"
	StateFailed       GameState = "FAILED"
	StateUnknown      GameState = "UNKNOWN"
)

type Game struct {
	ID         string    `json:"id"`
	Title      string    `json:"title"`
	Format     string    `json:"format,omitempty"`
	Version    string    `json:"version,omitempty"`
	Region     string    `json:"region,omitempty"`
	LocalPath  string    `json:"-"`
	RemotePath string    `json:"remote_path,omitempty"`
	Size       int64     `json:"size"`
	IconPath   string    `json:"-"`
	IconURL    string    `json:"icon_url,omitempty"`
	Installed  bool      `json:"installed"`
	State      GameState `json:"state"`
}

type Console struct {
	ID        string    `json:"id"`
	IP        string    `json:"ip"`
	Platform  Platform  `json:"platform,omitempty"`
	FTPPort   int       `json:"ftp_port,omitempty"`
	APIPort   int       `json:"api_port,omitempty"`
	FTPOnline bool      `json:"ftp_online"`
	Detected  bool      `json:"detected"`
	GameCount int       `json:"game_count"`
	LastSeen  time.Time `json:"last_seen"`
}

type QueueState string

type TransferDirection string

const (
	TransferUpload   TransferDirection = "upload"
	TransferDownload TransferDirection = "download"
)

const (
	QueueWaiting      QueueState = "WAITING"
	QueueStarting     QueueState = "STARTING"
	QueueTransferring QueueState = "TRANSFERRING"
	QueueVerifying    QueueState = "VERIFYING"
	QueueCompleted    QueueState = "COMPLETED"
	QueueFailed       QueueState = "FAILED"
	QueueCancelled    QueueState = "CANCELLED"
)

type Transfer struct {
	ID               string            `json:"id"`
	QueueID          string            `json:"queue_id"`
	Platform         Platform          `json:"platform,omitempty"`
	Direction        TransferDirection `json:"direction"`
	ConsoleIP        string            `json:"console_ip"`
	Game             Game              `json:"game"`
	State            QueueState        `json:"state"`
	CurrentFile      string            `json:"current_file,omitempty"`
	BytesTransferred int64             `json:"bytes_transferred"`
	TotalBytes       int64             `json:"total_bytes"`
	Percentage       float64           `json:"percentage"`
	Speed            int64             `json:"speed"`
	ETASeconds       int64             `json:"eta_seconds"`
	ElapsedSeconds   int64             `json:"elapsed_seconds"`
	Error            string            `json:"error,omitempty"`
	Attempts         int               `json:"attempts"`
	CreatedAt        time.Time         `json:"created_at"`
	StartedAt        *time.Time        `json:"started_at,omitempty"`
	FinishedAt       *time.Time        `json:"finished_at,omitempty"`
}

type Event struct {
	Type    string    `json:"type"`
	Time    time.Time `json:"time"`
	Payload any       `json:"payload,omitempty"`
}
