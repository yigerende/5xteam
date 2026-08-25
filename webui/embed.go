package webui

import "embed"

// Static contains the browser application bundled into the server binary.
//
//go:embed static/* static/assets/*
var Static embed.FS
