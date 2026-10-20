package server

import "net/http"

// glossaryTerm is one entry of the help page. ID is the stable anchor that
// inline "?" links on other pages point to.
type glossaryTerm struct {
	ID, Term, Text string
}

// glossary explains Treckrr's own terms in plain language. It holds no data,
// so the page is public: neighbors following a shared link can read it too.
var glossary = []glossaryTerm{
	{"bemessungsgrundlage", "Bemessungsgrundlage", "Die gemeinsam vereinbarte Preisliste: Kosten je PS-Stunde für jede Belastungsstufe und Kosten je Meter Arbeitsbreite und Stunde für jede Maschine. Sie gilt ab einem Jahr und kann von mehreren Abrechnungsjahren genutzt werden. Eine gesperrte Grundlage ändert sich nicht mehr."},
	{"abrechnungsjahr", "Abrechnungsjahr", "Das Jahr, in dem gebucht und abgerechnet wird. Es nutzt eine Bemessungsgrundlage und hat eigene Nachbarn. Ein abgeschlossenes Jahr nimmt keine neuen Buchungen an; Zahlungen bleiben möglich."},
	{"belastungsstufe", "Belastungsstufe", "Wie stark der Traktor gefordert ist, zum Beispiel leicht, mittel oder schwer. Der Traktorsatz je Stunde ist PS mal Kosten je PS-Stunde der Stufe."},
	{"maschinensatz", "Maschinensatz", "Der Stundensatz einer Maschine: Arbeitsbreite in Metern mal Kosten je Meter und Stunde."},
	{"gespann", "Gespann", "Eine feste Kombination aus Traktor, Belastungsstufe und Maschinen. Sein Stundensatz ist der Traktorsatz plus die Sätze aller Maschinen. Beim Buchen genügt dann die Auswahl des Gespanns."},
	{"buchung", "Buchung", "Eine erbrachte Arbeit für einen Nachbarn mit Datum, Stunden oder Menge und dem Satz, der beim Speichern galt. Spätere Preisänderungen ändern bestehende Buchungen nicht von selbst; dafür gibt es die Neuberechnung."},
	{"neuberechnung", "Neuberechnung", "Zeigt nach einer Preisänderung, welche noch nicht abgerechneten Buchungen sich ändern würden, und übernimmt sie nach Prüfung in einem Schritt. Festgeschriebene Rechnungen bleiben unberührt."},
	{"gegenleistung", "Gegenleistung und Verrechnung", "Arbeit, die ein Nachbar für dich erbracht hat, oder eine andere Kontoposition. Sie wird mit deinen Leistungen verrechnet und ändert den offenen Betrag, ohne selbst eine Rechnung zu sein."},
	{"guthaben", "Guthaben", "Ein negativer offener Betrag: Nicht der Nachbar schuldet dir etwas, sondern du ihm, etwa nach Gegenleistungen oder einer Überzahlung."},
	{"zwischenstand", "Zwischenstand", "Ein Link, mit dem ein Nachbar vor der Rechnung seine noch nicht abgerechneten Leistungen und seinen Kontostand sieht. Er kann den Stand bestätigen oder einen Einwand melden, solange eine Korrektur noch eine einfache Änderung ist."},
	{"festschreibung", "Festschreibung", "Das Ausstellen einer Rechnung: Inhalt, Nummer und Beträge werden eingefroren, wie es § 11 UStG verlangt. Danach ändern Buchungen oder Preise die Rechnung nicht mehr; Korrekturen laufen über Storno oder Gutschrift."},
	{"storno", "Storno", "Hebt eine festgeschriebene Rechnung vollständig auf und erzeugt dafür einen eigenen Beleg. Die Rechnungsnummer bleibt belegt; die Leistungen können danach neu abgerechnet werden."},
	{"gutschrift", "Gutschrift", "Mindert eine festgeschriebene Rechnung um einen Betrag, ohne sie aufzuheben, zum Beispiel bei einer Kulanz."},
	{"mahnwesen", "Mahnwesen", "Überfällige Rechnungen nach Zahlungsziel. Von dort gehen Zahlungserinnerung, 1. und 2. Mahnung mit den Gebühren aus den Betriebsdaten hinaus."},
}

// handleHelp renders the glossary.
func (s *Server) handleHelp(w http.ResponseWriter, r *http.Request) {
	data := pageData{"Title": "Hilfe & Begriffe", "Theme": s.themeFromCookie(r), "Glossary": glossary}
	if user := s.currentUser(r); user != nil {
		data = s.newPage(w, r, "Hilfe & Begriffe", "help")
		data["Glossary"] = glossary
	}
	w.Header().Set("Cache-Control", "no-store")
	s.render(w, r, "hilfe", data)
}
