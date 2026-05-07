package main

import (
	"testing"
)

func TestParseProfile_Named(t *testing.T) {
	tests := []struct {
		name      string
		profile   string
		wantNP    int
		wantSelNs int
		wantList  int
		wantGVKs  int
		wantNs    int
		wantErr   bool
	}{
		{"np_typical", "np-typical", 100, 0, 0, 10, 10, false},
		{"np_stress", "np-stress", 1000, 0, 0, 20, 50, false},
		{"cp_selector_typical", "cp-selector-typical", 0, 50, 0, 1, 1, false},
		{"cp_selector_stress", "cp-selector-stress", 0, 1000, 0, 1, 1, false},
		{"cp_list_typical", "cp-list-typical", 0, 0, 10, 1, 1, false},
		{"cp_list_stress", "cp-list-stress", 0, 0, 100, 1, 1, false},
		{"mixed_typical", "mixed-typical", 100, 50, 10, 10, 10, false},
		{"mixed_stress", "mixed-stress", 1000, 500, 100, 20, 50, false},
		{"full_is_expanded_elsewhere", "full", 0, 0, 0, 0, 0, true},
		{"fast_is_expanded_elsewhere", "fast", 0, 0, 0, 0, 0, true},
		{"invalid", "nope", 0, 0, 0, 0, 0, true},
		{"empty", "", 0, 0, 0, 0, 0, true},
		// Stale names from the v0.2 profile set must no longer parse.
		{"legacy_small_rejected", "small", 0, 0, 0, 0, 0, true},
		{"legacy_medium_rejected", "medium", 0, 0, 0, 0, 0, true},
		{"legacy_selector_rejected", "selector", 0, 0, 0, 0, 0, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ParseProfile(tc.profile, ProfileOverrides{})
			if tc.wantErr {
				if err == nil {
					t.Fatalf("want error, got nil (profile=%+v)", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got.NamespacedProjections != tc.wantNP {
				t.Errorf("NamespacedProjections: got %d, want %d", got.NamespacedProjections, tc.wantNP)
			}
			if got.SelectorNamespaces != tc.wantSelNs {
				t.Errorf("SelectorNamespaces: got %d, want %d", got.SelectorNamespaces, tc.wantSelNs)
			}
			if got.ListNamespaces != tc.wantList {
				t.Errorf("ListNamespaces: got %d, want %d", got.ListNamespaces, tc.wantList)
			}
			if got.GVKs != tc.wantGVKs {
				t.Errorf("GVKs: got %d, want %d", got.GVKs, tc.wantGVKs)
			}
			if got.Namespaces != tc.wantNs {
				t.Errorf("Namespaces: got %d, want %d", got.Namespaces, tc.wantNs)
			}
			if got.Name != tc.profile {
				t.Errorf("Name: got %q, want %q", got.Name, tc.profile)
			}
		})
	}
}

func TestParseProfile_Custom(t *testing.T) {
	t.Run("rejects empty overrides", func(t *testing.T) {
		_, err := ParseProfile("custom", ProfileOverrides{})
		if err == nil {
			t.Fatal("want error for missing overrides, got nil")
		}
	})
	t.Run("rejects gvks=0", func(t *testing.T) {
		_, err := ParseProfile("custom", ProfileOverrides{NamespacedProjections: 5, Namespaces: 1})
		if err == nil {
			t.Fatal("want error for gvks=0, got nil")
		}
	})
	t.Run("rejects no shape enabled", func(t *testing.T) {
		_, err := ParseProfile("custom", ProfileOverrides{GVKs: 1, Namespaces: 1})
		if err == nil {
			t.Fatal("want error when no shape is enabled, got nil")
		}
	})
	t.Run("rejects NP without namespaces", func(t *testing.T) {
		_, err := ParseProfile("custom", ProfileOverrides{NamespacedProjections: 5, GVKs: 1})
		if err == nil {
			t.Fatal("want error for NP > 0 with namespaces=0, got nil")
		}
	})
	t.Run("accepts NP-only", func(t *testing.T) {
		got, err := ParseProfile("custom", ProfileOverrides{
			NamespacedProjections: 50, GVKs: 5, Namespaces: 5,
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got.NamespacedProjections != 50 || got.GVKs != 5 || got.Namespaces != 5 {
			t.Errorf("overrides not applied: %+v", got)
		}
	})
	t.Run("accepts CP-selector-only without --namespaces", func(t *testing.T) {
		got, err := ParseProfile("custom", ProfileOverrides{
			SelectorNamespaces: 25, GVKs: 1,
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got.SelectorNamespaces != 25 || got.NamespacedProjections != 0 {
			t.Errorf("overrides not applied: %+v", got)
		}
	})
	t.Run("accepts CP-list-only without --namespaces", func(t *testing.T) {
		got, err := ParseProfile("custom", ProfileOverrides{
			ListNamespaces: 7, GVKs: 1,
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got.ListNamespaces != 7 {
			t.Errorf("overrides not applied: %+v", got)
		}
	})
	t.Run("accepts mixed shape", func(t *testing.T) {
		got, err := ParseProfile("custom", ProfileOverrides{
			NamespacedProjections: 10, SelectorNamespaces: 3, ListNamespaces: 2,
			GVKs: 2, Namespaces: 2,
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got.NamespacedProjections != 10 || got.SelectorNamespaces != 3 || got.ListNamespaces != 2 {
			t.Errorf("overrides not applied: %+v", got)
		}
	})
}

func TestExpandFull(t *testing.T) {
	ps := ExpandFull()
	wantNames := []string{
		"np-typical", "np-stress",
		"cp-selector-typical", "cp-selector-stress",
		"cp-list-typical", "cp-list-stress",
		"mixed-typical", "mixed-stress",
	}
	if len(ps) != len(wantNames) {
		t.Fatalf("want %d profiles in full, got %d", len(wantNames), len(ps))
	}
	for i, p := range ps {
		if p.Name != wantNames[i] {
			t.Errorf("profile[%d].Name: got %q, want %q", i, p.Name, wantNames[i])
		}
	}
}

// TestExpandFull_ReturnsCopy ensures a fresh slice is returned so mutations
// by callers don't leak into the package-level namedProfiles state — which
// would corrupt subsequent ParseProfile lookups.
func TestExpandFull_ReturnsCopy(t *testing.T) {
	a := ExpandFull()
	if len(a) == 0 {
		t.Fatal("ExpandFull returned empty slice")
	}
	a[0].Name = "tampered"
	b := ExpandFull()
	if b[0].Name == "tampered" {
		t.Errorf("ExpandFull leaks namedProfiles state: a mutation leaked into b[0]=%q", b[0].Name)
	}
}

func TestExpandFast(t *testing.T) {
	ps, err := ExpandFast()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	wantNames := []string{
		"np-typical",
		"cp-selector-typical",
		"cp-list-typical",
		"mixed-typical",
	}
	if len(ps) != len(wantNames) {
		t.Fatalf("want %d profiles in fast, got %d", len(wantNames), len(ps))
	}
	for i, p := range ps {
		if p.Name != wantNames[i] {
			t.Errorf("profile[%d].Name: got %q, want %q", i, p.Name, wantNames[i])
		}
	}
}
