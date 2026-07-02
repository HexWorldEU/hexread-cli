package version

import "testing"

// Get must pass the build vars straight through, tagged with the service name.
func TestGet(t *testing.T) {
	info := Get("hexread-cli")
	if info.Service != "hexread-cli" {
		t.Errorf("Service = %q, want hexread-cli", info.Service)
	}
	if info.Version != Version || info.Commit != Commit || info.Date != Date {
		t.Errorf("Get build vars = %q/%q/%q, want %q/%q/%q",
			info.Version, info.Commit, info.Date, Version, Commit, Date)
	}
}
