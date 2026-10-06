// Package announcements owns scheduled messages shown to administrators and customers.
package announcements

import "time"

type Level string

const (
	LevelInfo        Level = "info"
	LevelWarning     Level = "warning"
	LevelMaintenance Level = "maintenance"
)

func (l Level) Valid() bool {
	return l == LevelInfo || l == LevelWarning || l == LevelMaintenance
}

type Announcement struct {
	ID        string     `json:"id"`
	Title     string     `json:"title"`
	Content   string     `json:"content"`
	Level     Level      `json:"level"`
	Enabled   bool       `json:"enabled"`
	StartsAt  *time.Time `json:"starts_at"`
	EndsAt    *time.Time `json:"ends_at"`
	SortOrder int        `json:"sort_order"`
	Revision  int64      `json:"revision"`
	CreatedAt time.Time  `json:"created_at"`
	UpdatedAt time.Time  `json:"updated_at"`
}

func (a Announcement) Active(now time.Time) bool {
	return a.Enabled && (a.StartsAt == nil || !a.StartsAt.After(now)) && (a.EndsAt == nil || a.EndsAt.After(now))
}

type Request struct {
	Title     string     `json:"title"`
	Content   string     `json:"content"`
	Level     Level      `json:"level"`
	Enabled   bool       `json:"enabled"`
	StartsAt  *time.Time `json:"starts_at"`
	EndsAt    *time.Time `json:"ends_at"`
	SortOrder int        `json:"sort_order"`
	Revision  int64      `json:"revision"`
}
