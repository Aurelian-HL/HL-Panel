// Package siteconfig owns operator-managed public presentation settings.
package siteconfig

import "time"

const GlobalSettingsID = "global"

type Theme string

const (
	ThemeClassic     Theme = "classic"
	ThemeTransparent Theme = "transparent"
)

func (t Theme) Valid() bool {
	return t == ThemeClassic || t == ThemeTransparent
}

type Settings struct {
	ID                 string    `json:"id"`
	SiteName           string    `json:"site_name"`
	PanelTitle         string    `json:"panel_title"`
	PublicDescription  string    `json:"public_description"`
	SupportURL         string    `json:"support_url"`
	Theme              Theme     `json:"theme"`
	BackgroundImageURL string    `json:"background_image_url"`
	Revision           int64     `json:"revision"`
	UpdatedAt          time.Time `json:"updated_at"`
}

type Request struct {
	SiteName           string `json:"site_name"`
	PanelTitle         string `json:"panel_title"`
	PublicDescription  string `json:"public_description"`
	SupportURL         string `json:"support_url"`
	Theme              Theme  `json:"theme"`
	BackgroundImageURL string `json:"background_image_url"`
	Revision           int64  `json:"revision"`
}

func DefaultSettings() Settings {
	return Settings{
		ID:         GlobalSettingsID,
		SiteName:   "XZPanel",
		PanelTitle: "线路管理面板",
		Theme:      ThemeClassic,
	}
}
