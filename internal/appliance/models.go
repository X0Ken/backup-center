package appliance

import "time"

type User struct {
	ID         uint      `json:"id" gorm:"primaryKey"`
	Username   string    `json:"username" gorm:"uniqueIndex;not null"`
	Password   string    `json:"-"`
	Role       string    `json:"role"`
	Disabled   bool      `json:"disabled"`
	MustChange bool      `json:"must_change"`
	CreatedAt  time.Time `json:"created_at"`
}
type Session struct {
	ID        string `gorm:"primaryKey"`
	UserID    uint   `gorm:"index"`
	CSRF      string
	ExpiresAt time.Time `gorm:"index"`
}
type Repository struct {
	ID          uint      `json:"id" gorm:"primaryKey"`
	Name        string    `json:"name" gorm:"uniqueIndex;not null"`
	Dataset     string    `json:"dataset" gorm:"uniqueIndex;not null"`
	Kind        string    `json:"kind"`
	Description string    `json:"description"`
	CreatedAt   time.Time `json:"created_at"`
}
type Policy struct {
	ID           uint      `json:"id" gorm:"primaryKey"`
	RepositoryID uint      `json:"repository_id" gorm:"uniqueIndex;not null"`
	Cron         string    `json:"cron"`
	Enabled      bool      `json:"enabled"`
	KeepHourly   int       `json:"keep_hourly"`
	KeepDaily    int       `json:"keep_daily"`
	KeepWeekly   int       `json:"keep_weekly"`
	KeepMonthly  int       `json:"keep_monthly"`
	KeepYearly   int       `json:"keep_yearly"`
	Recursive    bool      `json:"recursive"`
	UpdatedAt    time.Time `json:"updated_at"`
}
type Job struct {
	ID         uint       `json:"id" gorm:"primaryKey"`
	Kind       string     `json:"kind"`
	Target     string     `json:"target"`
	Payload    string     `json:"-"`
	Status     string     `json:"status" gorm:"index"`
	Error      string     `json:"error"`
	Result     string     `json:"result"`
	UserID     uint       `json:"user_id"`
	CreatedAt  time.Time  `json:"created_at"`
	StartedAt  *time.Time `json:"started_at"`
	FinishedAt *time.Time `json:"finished_at"`
}
type Audit struct {
	ID        uint      `json:"id" gorm:"primaryKey"`
	UserID    uint      `json:"user_id"`
	Username  string    `json:"username"`
	Action    string    `json:"action"`
	Target    string    `json:"target"`
	Detail    string    `json:"detail"`
	IP        string    `json:"ip"`
	CreatedAt time.Time `json:"created_at"`
}
type Export struct {
	ID           uint      `json:"id" gorm:"primaryKey"`
	RepositoryID uint      `json:"repository_id" gorm:"uniqueIndex"`
	Protocol     string    `json:"protocol"`
	Clients      string    `json:"clients"`
	ReadOnly     bool      `json:"read_only"`
	Enabled      bool      `json:"enabled"`
	IQN          string    `json:"iqn"`
	Initiators   string    `json:"initiators"`
	UpdatedAt    time.Time `json:"updated_at"`
}
type Replication struct {
	ID              uint       `json:"id" gorm:"primaryKey"`
	RepositoryID    uint       `json:"repository_id"`
	Host            string     `json:"host"`
	User            string     `json:"user"`
	Port            int        `json:"port"`
	TargetDataset   string     `json:"target_dataset"`
	Cron            string     `json:"cron"`
	Enabled         bool       `json:"enabled"`
	AfterSnapshot   bool       `json:"after_snapshot"`
	LastSnapshot    string     `json:"last_snapshot"`
	LastGUID        string     `json:"last_guid"`
	LastRunAt       *time.Time `json:"last_run_at"`
	LastError       string     `json:"last_error"`
	Bytes           int64      `json:"bytes"`
	DurationSeconds float64    `json:"duration_seconds"`
}
type Alert struct {
	ID           uint      `json:"id" gorm:"primaryKey"`
	Key          string    `json:"key" gorm:"uniqueIndex"`
	Severity     string    `json:"severity"`
	Message      string    `json:"message"`
	Active       bool      `json:"active"`
	Acknowledged bool      `json:"acknowledged"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}
type Metric struct {
	ID         uint      `json:"-" gorm:"primaryKey"`
	CreatedAt  time.Time `json:"time" gorm:"index"`
	Used       uint64    `json:"used"`
	Size       uint64    `json:"size"`
	ReadBytes  uint64    `json:"read_bytes"`
	WriteBytes uint64    `json:"write_bytes"`
	ReadOps    uint64    `json:"read_ops"`
	WriteOps   uint64    `json:"write_ops"`
	ARCHit     float64   `json:"arc_hit"`
}
type Setting struct {
	Key   string `gorm:"primaryKey"`
	Value string
}
