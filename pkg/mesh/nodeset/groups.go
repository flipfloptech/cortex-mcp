package nodeset

// GroupResolver resolves @group references to node sets.
// Consumers implement this interface to integrate with their
// node inventory system.
type GroupResolver interface {
	// Resolve returns the nodes belonging to a group.
	// The source parameter is the group source (before the colon),
	// empty string for the default source.
	// The group parameter is the group name.
	Resolve(source, group string) ([]string, error)

	// List returns all available group names for a source.
	// Empty source means the default source.
	List(source string) ([]string, error)
}
