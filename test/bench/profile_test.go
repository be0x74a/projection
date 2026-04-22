package main

import (
	"testing"
)

func TestParseProfile_Named(t *testing.T) {
	tests := []struct {
		name      string
		profile   string
		wantProjs int
		wantGVKs  int
		wantNs    int
		wantSelNs int
		wantErr   bool
	}{
		{"small", "small", 100, 10, 10, 0, false},
		{"medium", "medium", 1000, 20, 50, 0, false},
		{"selector", "selector", 1, 1, 1, 100, false},
		{"full_is_expanded_elsewhere", "full", 0, 0, 0, 0, true}, // full is not a single profile
		{"invalid", "nope", 0, 0, 0, 0, true},
		{"empty", "", 0, 0, 0, 0, true},
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
			if got.Projections != tc.wantProjs {
				t.Errorf("Projections: got %d, want %d", got.Projections, tc.wantProjs)
			}
			if got.GVKs != tc.wantGVKs {
				t.Errorf("GVKs: got %d, want %d", got.GVKs, tc.wantGVKs)
			}
			if got.Namespaces != tc.wantNs {
				t.Errorf("Namespaces: got %d, want %d", got.Namespaces, tc.wantNs)
			}
			if got.SelectorNamespaces != tc.wantSelNs {
				t.Errorf("SelectorNamespaces: got %d, want %d", got.SelectorNamespaces, tc.wantSelNs)
			}
		})
	}
}

func TestParseProfile_Custom(t *testing.T) {
	t.Run("requires all overrides", func(t *testing.T) {
		_, err := ParseProfile("custom", ProfileOverrides{})
		if err == nil {
			t.Fatal("want error for missing overrides, got nil")
		}
	})
	t.Run("accepts full override set", func(t *testing.T) {
		got, err := ParseProfile("custom", ProfileOverrides{
			Projections: 50, GVKs: 5, Namespaces: 5,
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got.Projections != 50 || got.GVKs != 5 || got.Namespaces != 5 {
			t.Errorf("overrides not applied: %+v", got)
		}
	})
}

func TestExpandFull(t *testing.T) {
	ps, err := ExpandFull()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(ps) != 3 {
		t.Fatalf("want 3 profiles in full, got %d", len(ps))
	}
	wantNames := []string{"small", "medium", "selector"}
	for i, p := range ps {
		if p.Name != wantNames[i] {
			t.Errorf("profile[%d].Name: got %q, want %q", i, p.Name, wantNames[i])
		}
	}
}
