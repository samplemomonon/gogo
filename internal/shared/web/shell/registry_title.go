package shell

// registryTitle combines the service identity with an optional version for
// display ("gogo 1.0.0" or "gogo" when no version is set).
func registryTitle(service, version string) string {
	if version != "" {
		return service + " " + version
	}
	return service
}
