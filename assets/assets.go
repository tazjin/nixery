package assets

import "embed"

//go:embed *
var Files embed.FS

//go:embed index.html
var IndexTemplate string
