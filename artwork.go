package main

import "embed"

// The StarCraft II artwork is not stored in this repository. It is pulled from
// the pin in assets.lock by `make assets` (tools/fetchassets). This guard makes a
// plain `go build` fail with a useful message instead of producing a window with
// missing images.
//
//go:embed frontend/dist/leagues/*.png frontend/dist/races/*.png frontend/dist/bg.jpg
var artwork embed.FS
