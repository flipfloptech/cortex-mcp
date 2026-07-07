package registry

import (
	"encoding/json"
	"fmt"
	"strings"
)

// Category is the diagnostic tool taxonomy. It is a closed enum: tools
// declare their category with a typed constant, making invalid or
// misspelled categories (e.g. "Storage" vs "storage") a compile-time
// impossibility instead of a runtime filtering bug.
//
// On the wire (JSON, gossip, MCP responses) a Category is always
// serialized as its canonical lowercase name so LLM-facing payloads
// stay human-readable and remain compatible with mixed-version fleets.
type Category uint8

const (
	// CategoryUnknown is the zero-value sentinel. It is never a valid
	// category for a registered tool and is excluded from AllCategories.
	CategoryUnknown Category = iota
	CategorySystem
	CategoryCompute
	CategoryMemory
	CategoryNetwork
	CategoryStorage
	CategoryHardware
	CategoryLifecycle
)

// categoryNames maps each Category to its canonical wire name.
// Index positions must match the iota order above.
var categoryNames = [...]string{
	CategoryUnknown:   "unknown",
	CategorySystem:    "system",
	CategoryCompute:   "compute",
	CategoryMemory:    "memory",
	CategoryNetwork:   "network",
	CategoryStorage:   "storage",
	CategoryHardware:  "hardware",
	CategoryLifecycle: "lifecycle",
}

// String returns the canonical lowercase wire name for the category.
// Out-of-range values render as "unknown" rather than panicking.
func (c Category) String() string {
	if int(c) >= len(categoryNames) {
		return categoryNames[CategoryUnknown]
	}
	return categoryNames[c]
}

// ParseCategory converts a wire name into a Category. Matching is
// case-insensitive so legacy payloads ("Storage") and LLM-typed filters
// normalize instead of forking the taxonomy. Unknown names (including
// "unknown" itself) return an error listing every valid category so the
// caller can self-correct.
func ParseCategory(s string) (Category, error) {
	name := strings.ToLower(s)
	for _, c := range AllCategories() {
		if categoryNames[c] == name {
			return c, nil
		}
	}
	return CategoryUnknown, fmt.Errorf("registry: unknown category %q (valid: %s)", s, strings.Join(CategoryNames(), ", "))
}

// AllCategories returns every valid category in declaration order.
// The CategoryUnknown sentinel is excluded.
func AllCategories() []Category {
	return []Category{
		CategorySystem,
		CategoryCompute,
		CategoryMemory,
		CategoryNetwork,
		CategoryStorage,
		CategoryHardware,
		CategoryLifecycle,
	}
}

// CategoryNames returns the canonical wire names of every valid category
// in declaration order. Used to build help text and error messages.
func CategoryNames() []string {
	all := AllCategories()
	names := make([]string, len(all))
	for i, c := range all {
		names[i] = c.String()
	}
	return names
}

// MarshalJSON serializes the category as its lowercase wire name.
func (c Category) MarshalJSON() ([]byte, error) {
	return json.Marshal(c.String())
}

// UnmarshalJSON parses a category from its JSON string form,
// accepting legacy mixed-case names.
func (c *Category) UnmarshalJSON(data []byte) error {
	var s string
	if err := json.Unmarshal(data, &s); err != nil {
		return fmt.Errorf("registry: category must be a JSON string: %w", err)
	}
	parsed, err := ParseCategory(s)
	if err != nil {
		return err
	}
	*c = parsed
	return nil
}
