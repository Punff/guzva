package pages

import (
	"context"
	"strings"
	"testing"
)

// TestAboutCard pins the O salonu ruling (16 Oct 2026): the phone is a quiet
// action inside the card, not a full-width ink bar; the map is a plain link —
// an address search by default, the owner's pasted link when set — and a
// change of that link rewrites the page.
func TestAboutCard(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	if err := f.w.OnFrame(ctx, f.vid); err != nil {
		t.Fatal(err)
	}
	idx := f.readIndex(t)
	for _, want := range []string{
		`id="about-h">O salonu</h2>`,
		"Obala Kulina bana 3",
		`href="https://www.google.com/maps/search/?api=1&amp;query=Jadro%2C&#43;Obala&#43;Kulina&#43;bana&#43;3"`, // &#43; = "+" = space
		">Otvori u Google Kartama</a>",
		`<a class="btn btn--quiet" href="tel:&#43;38761234567">Nazovi`,
	} {
		if !strings.Contains(idx, want) {
			t.Errorf("about card lacks %q", want)
		}
	}
	if strings.Contains(idx, "btn--block bg-ink") {
		t.Error("the call button is still the full-width ink bar")
	}
	if strings.Contains(idx, "<iframe") {
		t.Error("the page embeds something — the map must stay a link")
	}

	if err := f.st.SetMapsURL(ctx, f.vid, "https://maps.app.goo.gl/AbC123"); err != nil {
		t.Fatal(err)
	}
	if err := f.w.Refresh(ctx, f.vid); err != nil {
		t.Fatal(err)
	}
	if idx := f.readIndex(t); !strings.Contains(idx, `href="https://maps.app.goo.gl/AbC123"`) {
		t.Error("a new Maps link did not rewrite the page (missing from the state key?)")
	}
}
