package interfacedep

import "embed"

//go:embed api.go
var Source embed.FS
