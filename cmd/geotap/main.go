package main

import (
	"fmt"
	"os"

	"github.com/rendis/geotap/internal/tui"
)

var version = "dev"

func main() {
	if len(os.Args) > 1 && os.Args[0] != "" {
		switch os.Args[1] {
		case "scan":
			if err := runScan(os.Args[2:]); err != nil {
				fmt.Fprintf(os.Stderr, "error: %v\n", err)
				os.Exit(1)
			}
			return
		case "match":
			if err := runMatch(os.Args[2:]); err != nil {
				fmt.Fprintf(os.Stderr, "error: %v\n", err)
				os.Exit(1)
			}
			return
		case "export":
			if err := runExport(os.Args[2:]); err != nil {
				fmt.Fprintf(os.Stderr, "error: %v\n", err)
				os.Exit(1)
			}
			return
		case "version":
			fmt.Println("geotap " + version)
			return
		case "help", "--help", "-h":
			printUsage()
			return
		}
	}

	// No subcommand → launch TUI
	if err := tui.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

func printUsage() {
	fmt.Fprintf(os.Stderr, `geotap - Google Maps geographic data scanner

Usage:
  geotap                Launch interactive TUI
  geotap scan [flags]   Run headless scan
  geotap match [flags]  Search, fuzzy-match by name, fetch photos
  geotap export [flags] Export .db to CSV
  geotap version        Show version

Run 'geotap <command> --help' for flags.
`)
}
