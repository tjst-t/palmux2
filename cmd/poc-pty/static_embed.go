package main

import "embed"

// staticFiles embeds the cmd/poc-pty/static/ directory into the binary.
//
//go:embed static
var staticFiles embed.FS
