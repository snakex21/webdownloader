// Package webui owns the frontend assets embedded in the desktop application.
package webui

import (
	"embed"
	"encoding/json"
)

//go:embed web/*
var assets embed.FS

func HTML() ([]byte, error) {
	return assets.ReadFile("web/index.html")
}

func Translations() (map[string]any, error) {
	data, err := assets.ReadFile("web/i18n.json")
	if err != nil {
		return nil, err
	}

	translations := make(map[string]any)
	if err := json.Unmarshal(data, &translations); err != nil {
		return nil, err
	}
	return translations, nil
}
