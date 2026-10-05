// Package bundle concatenates ordered browser-asset source files.
package bundle

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
)

// Spec describes one generated asset and its ordered source parts.
type Spec struct {
	Output string
	Parts  []string
}

// Specs is the single source of truth for the generated CSS and JavaScript.
var Specs = []Spec{
	{
		Output: filepath.FromSlash("../static/css/app.css"),
		Parts: []string{
			filepath.FromSlash("css/00-tokens.css"),
			filepath.FromSlash("css/10-base.css"),
			filepath.FromSlash("css/20-shell.css"),
			filepath.FromSlash("css/30-components.css"),
			filepath.FromSlash("css/40-workflows.css"),
			filepath.FromSlash("css/50-responsive.css"),
		},
	},
	{
		Output: filepath.FromSlash("../static/js/app.js"),
		Parts: []string{
			filepath.FromSlash("js/00-core.js"),
			filepath.FromSlash("js/10-forms.js"),
			filepath.FromSlash("js/20-feedback.js"),
			filepath.FromSlash("js/30-shell.js"),
			filepath.FromSlash("js/40-documents.js"),
			filepath.FromSlash("js/50-operations.js"),
			filepath.FromSlash("js/60-navigation.js"),
		},
	},
}

// Generate rebuilds every output from its ordered source parts. Unchanged
// outputs are left untouched so synchronized workspaces do not churn files.
func Generate(root string) error {
	for _, spec := range Specs {
		want, err := assemble(root, spec)
		if err != nil {
			return err
		}
		output := filepath.Join(root, spec.Output)
		// #nosec G304 -- output is selected from the package-owned Specs table.
		have, err := os.ReadFile(output)
		if err == nil && bytes.Equal(have, want) {
			continue
		}
		if err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("read generated asset %s: %w", output, err)
		}
		// Generated CSS/JS are public web assets, not credentials or private data.
		// #nosec G306 -- browsers and the embedded server must be able to read them.
		if err := os.WriteFile(output, want, 0o644); err != nil {
			return fmt.Errorf("write generated asset %s: %w", output, err)
		}
	}
	return nil
}

// Verify reports whether every generated output exactly matches its sources.
func Verify(root string) error {
	for _, spec := range Specs {
		want, err := assemble(root, spec)
		if err != nil {
			return err
		}
		output := filepath.Join(root, spec.Output)
		// #nosec G304 -- output is selected from the package-owned Specs table.
		have, err := os.ReadFile(output)
		if err != nil {
			return fmt.Errorf("read generated asset %s: %w", output, err)
		}
		if !bytes.Equal(have, want) {
			return fmt.Errorf("generated asset %s is stale; run go generate ./internal/web/assets", output)
		}
	}
	return nil
}

func assemble(root string, spec Spec) ([]byte, error) {
	var out []byte
	for _, part := range spec.Parts {
		path := filepath.Join(root, part)
		// #nosec G304 -- part is selected from the package-owned Specs table.
		body, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("read asset source %s: %w", path, err)
		}
		out = append(out, body...)
	}
	return out, nil
}
