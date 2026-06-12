package runner

import (
	"fmt"
	"sort"
)

type Registry struct {
	runners map[string]Runner
}

func NewRegistry() *Registry {
	return &Registry{runners: map[string]Runner{}}
}

func (r *Registry) Register(run Runner) error {
	if run == nil {
		return fmt.Errorf("runner is nil")
	}
	name := run.Name()
	if name == "" {
		return fmt.Errorf("runner has empty name")
	}
	if _, exists := r.runners[name]; exists {
		return fmt.Errorf("runner %q registered twice", name)
	}
	r.runners[name] = run
	return nil
}

func (r *Registry) Get(name string) (Runner, bool) {
	if r == nil {
		return nil, false
	}
	run, ok := r.runners[name]
	return run, ok
}

func (r *Registry) Names() []string {
	if r == nil {
		return nil
	}
	names := make([]string, 0, len(r.runners))
	for name := range r.runners {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func (r *Registry) Manifests() []CapabilityManifest {
	names := r.Names()
	out := make([]CapabilityManifest, 0, len(names))
	for _, name := range names {
		out = append(out, r.runners[name].Manifest())
	}
	return out
}
