package server

import "rsc.io/qr"

// qrPNG renders the given text as a QR-code PNG. Generated locally (no external
// service). It is served from an endpoint as image/png so the template can use
// a plain same-origin <img src> (permitted by the `img-src 'self'` CSP) without
// an inline data: URI.
func qrPNG(text string) ([]byte, error) {
	code, err := qr.Encode(text, qr.M)
	if err != nil {
		return nil, err
	}
	return code.PNG(), nil
}
