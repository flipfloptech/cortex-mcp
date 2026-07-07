package registry

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestCategory_String verifies every defined category renders its canonical
// lowercase wire name, and that the zero value is distinguishable.
func TestCategory_String(t *testing.T) {
	t.Parallel()

	cases := []struct {
		cat  Category
		want string
	}{
		{CategoryUnknown, "unknown"},
		{CategorySystem, "system"},
		{CategoryCompute, "compute"},
		{CategoryMemory, "memory"},
		{CategoryNetwork, "network"},
		{CategoryStorage, "storage"},
		{CategoryHardware, "hardware"},
		{CategoryLifecycle, "lifecycle"},
	}

	for _, tc := range cases {
		if got := tc.cat.String(); got != tc.want {
			t.Errorf("Category(%d).String() = %q, want %q", tc.cat, got, tc.want)
		}
	}

	// Out-of-range values must not panic and must be identifiable.
	if got := Category(250).String(); got != "unknown" {
		t.Errorf("out-of-range Category.String() = %q, want %q", got, "unknown")
	}
}

// TestParseCategory verifies parsing is case-insensitive (the "Storage" vs
// "storage" bug class) and rejects unknown names.
func TestParseCategory(t *testing.T) {
	t.Parallel()

	valid := []struct {
		in   string
		want Category
	}{
		{"system", CategorySystem},
		{"compute", CategoryCompute},
		{"memory", CategoryMemory},
		{"network", CategoryNetwork},
		{"storage", CategoryStorage},
		{"hardware", CategoryHardware},
		{"lifecycle", CategoryLifecycle},
		// Case-insensitivity: mixed case must normalize, never fork a category.
		{"Storage", CategoryStorage},
		{"STORAGE", CategoryStorage},
		{"System", CategorySystem},
	}
	for _, tc := range valid {
		got, err := ParseCategory(tc.in)
		if err != nil {
			t.Errorf("ParseCategory(%q) unexpected error: %v", tc.in, err)
			continue
		}
		if got != tc.want {
			t.Errorf("ParseCategory(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}

	invalid := []string{"", "lustre", "Storage ", "disk", "unknown"}
	for _, in := range invalid {
		if got, err := ParseCategory(in); err == nil {
			t.Errorf("ParseCategory(%q) = %v, want error", in, got)
		}
	}
}

// TestParseCategory_ErrorListsValidValues ensures the error message helps the
// caller (an LLM) self-correct by listing every valid category.
func TestParseCategory_ErrorListsValidValues(t *testing.T) {
	t.Parallel()

	_, err := ParseCategory("lustre")
	if err == nil {
		t.Fatal("ParseCategory(\"lustre\") expected error, got nil")
	}
	for _, c := range AllCategories() {
		if !strings.Contains(err.Error(), c.String()) {
			t.Errorf("error %q does not mention valid category %q", err.Error(), c.String())
		}
	}
}

// TestCategory_JSONRoundTrip verifies categories marshal to lowercase JSON
// strings and unmarshal back, including from legacy mixed-case payloads.
func TestCategory_JSONRoundTrip(t *testing.T) {
	t.Parallel()

	for _, c := range AllCategories() {
		data, err := json.Marshal(c)
		if err != nil {
			t.Fatalf("Marshal(%v) error: %v", c, err)
		}
		want := `"` + c.String() + `"`
		if string(data) != want {
			t.Errorf("Marshal(%v) = %s, want %s", c, data, want)
		}

		var back Category
		if err := json.Unmarshal(data, &back); err != nil {
			t.Fatalf("Unmarshal(%s) error: %v", data, err)
		}
		if back != c {
			t.Errorf("round-trip: got %v, want %v", back, c)
		}
	}

	// Legacy mixed-case payloads (older fleet nodes) must still decode.
	var c Category
	if err := json.Unmarshal([]byte(`"Storage"`), &c); err != nil {
		t.Fatalf("Unmarshal legacy \"Storage\" error: %v", err)
	}
	if c != CategoryStorage {
		t.Errorf("Unmarshal legacy \"Storage\" = %v, want CategoryStorage", c)
	}

	// Unknown names and non-string JSON must fail loudly.
	if err := json.Unmarshal([]byte(`"lustre"`), &c); err == nil {
		t.Error("Unmarshal(\"lustre\") expected error, got nil")
	}
	if err := json.Unmarshal([]byte(`7`), &c); err == nil {
		t.Error("Unmarshal(7) expected error, got nil")
	}
}

// TestAllCategories verifies the enumeration is complete, ordered, and
// excludes the unknown sentinel.
func TestAllCategories(t *testing.T) {
	t.Parallel()

	all := AllCategories()
	want := []Category{
		CategorySystem,
		CategoryCompute,
		CategoryMemory,
		CategoryNetwork,
		CategoryStorage,
		CategoryHardware,
		CategoryLifecycle,
	}

	if len(all) != len(want) {
		t.Fatalf("AllCategories() len = %d, want %d", len(all), len(want))
	}
	for i := range want {
		if all[i] != want[i] {
			t.Errorf("AllCategories()[%d] = %v, want %v", i, all[i], want[i])
		}
	}
	for _, c := range all {
		if c == CategoryUnknown {
			t.Error("AllCategories() must not include CategoryUnknown")
		}
	}

	// Every category name must be unique — a duplicate String() would
	// silently merge two categories on the wire.
	seen := make(map[string]bool, len(all))
	for _, c := range all {
		if seen[c.String()] {
			t.Errorf("duplicate category name %q", c.String())
		}
		seen[c.String()] = true
	}
}

// TestCategoryNames verifies the human-readable list used in help text and
// error messages stays in sync with the enum.
func TestCategoryNames(t *testing.T) {
	t.Parallel()

	names := CategoryNames()
	all := AllCategories()
	if len(names) != len(all) {
		t.Fatalf("CategoryNames() len = %d, want %d", len(names), len(all))
	}
	for i, c := range all {
		if names[i] != c.String() {
			t.Errorf("CategoryNames()[%d] = %q, want %q", i, names[i], c.String())
		}
	}
}
