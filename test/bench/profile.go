package main

import "fmt"

// Profile is a fully-resolved operating point. All counts are the number of
// objects the harness will create.
type Profile struct {
	Name               string
	Projections        int
	GVKs               int
	Namespaces         int // source+dest namespaces, except for selector
	SelectorNamespaces int // > 0 means selector profile (1 Projection fans out)
}

// ProfileOverrides carries the --projections / --gvks / --namespaces /
// --selector-ns flag values for the `custom` profile.
type ProfileOverrides struct {
	Projections        int
	GVKs               int
	Namespaces         int
	SelectorNamespaces int
}

// ParseProfile resolves a --profile flag value (and its overrides for
// `custom`) to a Profile. Returns an error for unknown names, `full` (which
// must be expanded via ExpandFull), or incomplete `custom` overrides.
func ParseProfile(name string, ov ProfileOverrides) (Profile, error) {
	switch name {
	case "small":
		return Profile{Name: "small", Projections: 100, GVKs: 10, Namespaces: 10}, nil
	case "medium":
		return Profile{Name: "medium", Projections: 1000, GVKs: 20, Namespaces: 50}, nil
	case "selector":
		return Profile{Name: "selector", Projections: 1, GVKs: 1, Namespaces: 1, SelectorNamespaces: 100}, nil
	case "custom":
		if ov.Projections <= 0 || ov.GVKs <= 0 || ov.Namespaces <= 0 {
			return Profile{}, fmt.Errorf("custom profile requires --projections, --gvks, and --namespaces (>0)")
		}
		return Profile{
			Name:               "custom",
			Projections:        ov.Projections,
			GVKs:               ov.GVKs,
			Namespaces:         ov.Namespaces,
			SelectorNamespaces: ov.SelectorNamespaces,
		}, nil
	case "full":
		return Profile{}, fmt.Errorf("profile=full is a sequence of profiles; call ExpandFull() instead")
	case "":
		return Profile{}, fmt.Errorf("profile name is empty")
	default:
		return Profile{}, fmt.Errorf("unknown profile %q (want: small, medium, selector, full, custom)", name)
	}
}

// ExpandFull returns the sequence of profiles that `full` runs.
func ExpandFull() ([]Profile, error) {
	names := []string{"small", "medium", "selector"}
	out := make([]Profile, 0, len(names))
	for _, n := range names {
		p, err := ParseProfile(n, ProfileOverrides{})
		if err != nil {
			return nil, fmt.Errorf("expanding full: %w", err)
		}
		out = append(out, p)
	}
	return out, nil
}
