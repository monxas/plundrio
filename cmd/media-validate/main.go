// media-validate is the same bounded admission implementation used by Plundrio,
// packaged for the scanner. It inspects one existing local file and its journal.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"github.com/elsbrock/plundrio/internal/mediavalidation"
	"os"
)

func main() {
	if mediavalidation.HandleProbeHelper(os.Args) {
		return
	}
	root := flag.String("root", "/downloads", "Allowed local root")
	state := flag.String("state", "/config/media-validation", "Private journal")
	path := flag.String("path", "", "Existing local file")
	flag.Parse()
	if *path == "" {
		os.Exit(3)
	}
	result := mediavalidation.New(*root, *state, mediavalidation.ProcessProbe).Check(context.Background(), *path)
	_ = json.NewEncoder(os.Stdout).Encode(result)
	if !result.Accepted() {
		if result.Status == "pending" {
			os.Exit(3)
		}
		os.Exit(2)
	}
}
