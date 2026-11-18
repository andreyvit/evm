package importfixture

import "embed"

//go:embed api.go
var Source embed.FS
