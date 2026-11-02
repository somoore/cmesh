package profile

import (
	"strings"
	"testing"
)

// A profile name is interpolated into `[profiles.<name>]`, so a name carrying a
// bracket/newline can append an arbitrary table (e.g. [peers.evil]) to the
// operator's config. Generate must reject it before anything is rendered.
func TestGenerateRejectsTOMLInjectionInNames(t *testing.T) {
	bad := []GenerateOptions{
		{Name: "x]\nrunner=\"shell\"\n[peers.evil]\nname=\"evil\"\naddress=\"100.64.0.9\"\nport=7778\n[profiles.zzz"},
		{Name: "x]\n[peers.evil"},
		{Name: "my.profile"},
		{Name: "with space"},
		{Name: "quote\"name"},
		{Name: "bracket]name"},
		{Runner: "shell]\n[peers.evil"},
		{Runner: "bad runner"},
	}
	for _, opts := range bad {
		if _, err := Generate(Query, opts); err == nil {
			t.Errorf("Generate(%q/%q) accepted an unsafe TOML key", opts.Name, opts.Runner)
		}
	}

	tmpl, err := Generate(Query, GenerateOptions{Name: "my-custom-profile"})
	if err != nil {
		t.Fatalf("Generate rejected a valid name: %v", err)
	}
	if tmpl.Name != "my-custom-profile" {
		t.Fatalf("template = %+v", tmpl)
	}
	rendered := RenderTOML(tmpl)
	if strings.Contains(rendered, "[peers.") {
		t.Fatalf("rendered output injected a peer table:\n%s", rendered)
	}
}
