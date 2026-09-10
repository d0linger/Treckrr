//go:build integration

package server

import (
	"fmt"
	"net/url"
	"strings"
	"testing"
)

func TestNeighborRejectsInvalidPaymentTermIntegration(t *testing.T) {
	e := newItEnv(t)
	n, err := e.st.GetNeighbor(e.ctx, e.neighborID)
	if err != nil {
		t.Fatal(err)
	}
	for _, value := range []string{"-1", "366", "not-a-number", "99999999999999999999"} {
		t.Run(value, func(t *testing.T) {
			body := e.post(fmt.Sprintf("/neighbors/%d/update", n.ID), url.Values{
				"name": {"must not save"}, "payment_term_days": {value}, "origin": {"manage"},
			})
			if !strings.Contains(body, "Zahlungsziel muss eine ganze Zahl zwischen 0 und 365 Tagen sein.") {
				t.Error("missing payment-term validation message")
			}
			got, err := e.st.GetNeighbor(e.ctx, n.ID)
			if err != nil {
				t.Fatal(err)
			}
			if got.Name != n.Name || got.PaymentTermDays != nil {
				t.Error("invalid submission modified the neighbor")
			}
		})
	}
}
