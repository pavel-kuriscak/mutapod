package main

import (
	"github.com/mutapod/mutapod/internal/cli"

	// Register providers
	_ "github.com/mutapod/mutapod/internal/provider/azure"
	_ "github.com/mutapod/mutapod/internal/provider/gcp"
)

func main() {
	cli.Execute()
}
