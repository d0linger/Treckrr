package web

import (
	"regexp"
	"strings"
	"testing"

	"github.com/d0linger/treckrr/internal/models"
)

// TestBookingTimeHelperSubmitsNothing renders the booking form: writers get the
// Von / Bis / Pause and stopwatch helper inside the hours panels, and none of
// its controls has a name, so the submitted form, drafts and the offline queue
// stay unchanged. Viewers get no form and therefore no helper.
func TestBookingTimeHelperSubmitsNothing(t *testing.T) {
	t.Parallel()
	writer := accountPolishContent(t, "neighbor", accountPolishData(models.User{Role: models.RoleEditor}, "booking"))
	helper := regexp.MustCompile(`(?s)<details[^>]*data-time-helper[^>]*>.*?</details>`).FindString(writer)
	if helper == "" {
		t.Fatal("booking form lacks the time helper")
	}
	if !strings.Contains(helper, `data-booking-panel="equipment:out equipment:in labor:out labor:in"`) {
		t.Error("time helper must follow the hours panels")
	}
	for _, control := range []string{"data-time-from", "data-time-to", "data-time-break", "data-stopwatch-toggle"} {
		if !strings.Contains(helper, control) {
			t.Errorf("time helper lacks %s", control)
		}
	}
	if strings.Contains(helper, " name=") {
		t.Error("time helper controls must not be submitted with the booking")
	}
	viewer := accountPolishContent(t, "neighbor", accountPolishData(models.User{Role: models.RoleViewer}, "booking"))
	if strings.Contains(viewer, "data-time-helper") {
		t.Error("read-only account must not offer the time helper")
	}
}
