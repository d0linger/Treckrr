// Command concat rebuilds the embedded browser assets from ordered sources.
package main

import (
	"fmt"
	"os"

	"github.com/d0linger/treckrr/internal/web/assets/bundle"
)

func main() {
	if err := bundle.Generate("."); err != nil {
		_, _ = fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
