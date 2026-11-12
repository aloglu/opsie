// Command opsie is a dashboard for keeping an eye on, and managing, a Linux
// server from the terminal.
package main

import (
	"os"

	"github.com/aloglu/opsie/internal/cli"
)

// Set at build time with -ldflags "-X main.version=... -X main.channel=...".
// Release builds set channel to "release" and the Makefile sets it to "dev";
// `go install` leaves both empty and the version comes from the module.
var (
	version = ""
	channel = ""
)

func main() {
	os.Exit(cli.Main(os.Args[1:], version, channel))
}
