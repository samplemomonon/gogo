package shell

// RegistryView is the render model of one module-registry entry: exactly
// the fields the registry page displays. It is deliberately a leaf data
// type — the shell renders module manifests without knowing the federation
// contract (module.Descriptor), so the web layer never imports the module
// layer. The gateway maps manifests to views at its composition boundary.
type RegistryView struct {
	// Name is the module's manifest name.
	Name string

	// Version is the module's manifest version.
	Version string

	// Entry is the module's federated entry path.
	Entry string

	// Description is the module's manifest description.
	Description string
}
