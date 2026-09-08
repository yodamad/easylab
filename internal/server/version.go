package server

import "html/template"

// Version is the running EasyLab build version. It is injected at build time via:
//
//	-ldflags "-X easylab/internal/server.Version=<version>"
//
// and defaults to "dev" for builds that skip ldflags (e.g. `go run`, `make dev`).
var Version = "dev"

// templateFuncMap is shared by every html/template set that parses web/base.html,
// so the "appVersion" function stays available regardless of which page templates
// are parsed alongside it.
var templateFuncMap = template.FuncMap{
	"appVersion": func() string { return Version },
	// asset renders "/static/x.js" as "/static/x.js?v=<content hash>" so a deploy
	// invalidates the browser's copy on its own — see assetURL in assets.go.
	"asset": assetURL,
}
