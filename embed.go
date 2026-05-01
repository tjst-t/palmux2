package palmux2

import "embed"

//go:embed all:frontend/dist
var FrontendFS embed.FS

// StaticFS serves third-party assets that ship with palmux2 itself —
// most notably the drawio webapp used by the Files-tab `.drawio` viewer
// (S010). Mounted at `/static/...` by `internal/server`. No auth gate
// because the contents are public OSS bundles, not user data.
//
//go:embed all:internal/static
var StaticFS embed.FS
