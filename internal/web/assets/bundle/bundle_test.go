package bundle

import "testing"

// TestGeneratedAssetsCurrent prevents source-layer changes from bypassing the
// embedded CSS and JavaScript delivered by the server.
func TestGeneratedAssetsCurrent(t *testing.T) {
	t.Parallel()
	if err := Verify(".."); err != nil {
		t.Fatal(err)
	}
}
