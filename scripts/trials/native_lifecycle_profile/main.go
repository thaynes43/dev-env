// Command native_lifecycle_profile renders the source-controlled, isolated
// lifecycle profile. It does not provision resources or validate signatures.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"

	"github.com/thaynes43/dev-env/internal/taskbudget"
)

func main() {
	image := flag.String("image", "", "reviewed signed immutable agent image digest")
	flag.Parse()
	if !taskbudget.ValidNativeFixtureImage(*image) || flag.NArg() != 0 {
		fmt.Fprintln(os.Stderr, "one reviewed image is required; this renderer never deploys")
		os.Exit(2)
	}
	objects := map[string]any{"apiVersion": "v1", "kind": "List", "items": []any{taskbudget.NativeFixturePod(*image), taskbudget.NativeFixturePolicy()}}
	e := json.NewEncoder(os.Stdout)
	e.SetIndent("", "  ")
	if err := e.Encode(objects); err != nil {
		fmt.Fprintln(os.Stderr, "profile encoding failed")
		os.Exit(1)
	}
}
