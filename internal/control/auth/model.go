package auth

import "time"

type Administrator struct {
	ID                 string    `json:"-"`
	Username           string    `json:"-"`
	PasswordHash       []byte    `json:"-"`
	CreatedAt          time.Time `json:"-"`
	MustChangePassword bool      `json:"-"`
}

type AdministratorView struct {
	ID                 string    `json:"id"`
	Username           string    `json:"username"`
	CreatedAt          time.Time `json:"created_at"`
	MustChangePassword bool      `json:"must_change_password"`
}

type Session struct {
	ID                 string
	AdminID            string
	TokenHash          string
	ExpiresAt          time.Time
	CreatedAt          time.Time
	MustChangePassword bool `json:"-"`
}

type LoginResult struct {
	AccessToken string            `json:"access_token"`
	ExpiresAt   time.Time         `json:"expires_at"`
	User        AdministratorView `json:"user"`
}
