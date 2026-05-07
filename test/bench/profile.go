package main

import "fmt"

// Profile is a fully-resolved operating point. All counts are the number of
// objects the harness will create. Topology is inferred from which fields
// are nonzero — a single profile may exercise NP, CP-selector, and CP-list
// shapes simultaneously (e.g. mixed-*).
type Profile struct {
	Name string
	// NamespacedProjections is the total number of single-target Projection
	// CRs (v0.3 namespaced kind, destination implicit = own namespace).
	NamespacedProjections int
	// SelectorNamespaces is the matching-namespace count for the single
	// CP-selector instance the profile creates. 0 disables that shape.
	SelectorNamespaces int
	// ListNamespaces is the explicit-namespace count for the single CP-list
	// instance the profile creates. 0 disables that shape.
	ListNamespaces int
	// GVKs is the count of distinct bench source GVKs to install.
	GVKs int
	// Namespaces is the source-side namespace count NP shapes use; ignored
	// when NamespacedProjections == 0. Must be >= 1 when NP is enabled.
	Namespaces int
}

// ProfileOverrides carries the flag values for the `custom` profile. Mirrors
// the Profile field set; ParseProfile validates the combination.
type ProfileOverrides struct {
	NamespacedProjections int
	SelectorNamespaces    int
	ListNamespaces        int
	GVKs                  int
	Namespaces            int
}

// namedProfiles is the ordered map of named operating points (excluding the
// meta-profiles `custom`, `full`, and `fast`). Order mirrors the matrix in
// CLAUDE.md / docs and `full`'s expansion.
var namedProfiles = []Profile{
	{Name: "np-typical", NamespacedProjections: 100, GVKs: 10, Namespaces: 10},
	{Name: "np-stress", NamespacedProjections: 1000, GVKs: 20, Namespaces: 50},
	{Name: "cp-selector-typical", SelectorNamespaces: 50, GVKs: 1, Namespaces: 1},
	{Name: "cp-selector-stress", SelectorNamespaces: 1000, GVKs: 1, Namespaces: 1},
	{Name: "cp-list-typical", ListNamespaces: 10, GVKs: 1, Namespaces: 1},
	{Name: "cp-list-stress", ListNamespaces: 100, GVKs: 1, Namespaces: 1},
	{Name: "mixed-typical", NamespacedProjections: 100, SelectorNamespaces: 50, ListNamespaces: 10, GVKs: 10, Namespaces: 10},
	{Name: "mixed-stress", NamespacedProjections: 1000, SelectorNamespaces: 500, ListNamespaces: 100, GVKs: 20, Namespaces: 50},
}

// fastProfiles is the subset `fast` expands to: the four *-typical shapes
// for ad-hoc local regression checks (no stress runs).
var fastProfiles = []string{
	"np-typical",
	"cp-selector-typical",
	"cp-list-typical",
	"mixed-typical",
}

// validProfileNames is the human-readable list emitted in error messages and
// the --profile flag help.
const validProfileNames = "np-typical, np-stress, cp-selector-typical, " +
	"cp-selector-stress, cp-list-typical, cp-list-stress, mixed-typical, " +
	"mixed-stress, fast, full, custom"

// ParseProfile resolves a --profile flag value (and its overrides for
// `custom`) to a Profile. Returns an error for unknown names, the
// meta-profiles `full` and `fast` (which must be expanded via ExpandFull /
// ExpandFast), or incomplete `custom` overrides.
func ParseProfile(name string, ov ProfileOverrides) (Profile, error) {
	if name == "" {
		return Profile{}, fmt.Errorf("profile name is empty")
	}
	for _, p := range namedProfiles {
		if p.Name == name {
			return p, nil
		}
	}
	switch name {
	case "custom":
		return parseCustomProfile(ov)
	case "full":
		return Profile{}, fmt.Errorf("profile=full is a sequence of profiles; call ExpandFull() instead")
	case "fast":
		return Profile{}, fmt.Errorf("profile=fast is a sequence of profiles; call ExpandFast() instead")
	}
	return Profile{}, fmt.Errorf("unknown profile %q (want: %s)", name, validProfileNames)
}

// parseCustomProfile validates a `custom` profile's overrides. At least one
// shape must be enabled, and any enabled shape must have its supporting
// fields set.
func parseCustomProfile(ov ProfileOverrides) (Profile, error) {
	if ov.GVKs <= 0 {
		return Profile{}, fmt.Errorf("custom profile requires --gvks > 0")
	}
	hasNP := ov.NamespacedProjections > 0
	hasSel := ov.SelectorNamespaces > 0
	hasList := ov.ListNamespaces > 0
	if !hasNP && !hasSel && !hasList {
		return Profile{}, fmt.Errorf("custom profile requires at least one of " +
			"--namespaced-projections, --selector-namespaces, --list-namespaces (>0)")
	}
	if hasNP && ov.Namespaces <= 0 {
		return Profile{}, fmt.Errorf("custom profile with --namespaced-projections > 0 also requires --namespaces > 0")
	}
	ns := ov.Namespaces
	if ns <= 0 {
		// CP-only shapes don't use the NP source-namespace fanout but the
		// field still feeds bootstrap; default to 1 to keep invariants happy.
		ns = 1
	}
	return Profile{
		Name:                  "custom",
		NamespacedProjections: ov.NamespacedProjections,
		SelectorNamespaces:    ov.SelectorNamespaces,
		ListNamespaces:        ov.ListNamespaces,
		GVKs:                  ov.GVKs,
		Namespaces:            ns,
	}, nil
}

// ExpandFull returns the sequence of profiles that `full` runs (all 8 named
// profiles in matrix order). The returned slice is a fresh copy so callers
// can mutate it without leaking into the package-level `namedProfiles`.
func ExpandFull() []Profile {
	out := make([]Profile, 0, len(namedProfiles))
	out = append(out, namedProfiles...)
	return out
}

// ExpandFast returns the four *-typical profiles for ad-hoc local regression
// checks. No stress shapes — keeps wall-clock low.
func ExpandFast() ([]Profile, error) {
	out := make([]Profile, 0, len(fastProfiles))
	for _, name := range fastProfiles {
		p, err := ParseProfile(name, ProfileOverrides{})
		if err != nil {
			return nil, fmt.Errorf("expanding fast: %w", err)
		}
		out = append(out, p)
	}
	return out, nil
}
