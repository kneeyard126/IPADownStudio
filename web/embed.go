package web

import "embed"

// StaticFS embeds the single-page web desktop frontend assets.
//
//go:embed index.html app.css i18n.js app.js
var StaticFS embed.FS
