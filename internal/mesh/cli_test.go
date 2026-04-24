package mesh

import (
	"bytes"
	"strings"
	"testing"
)

func BenchmarkRunConnectionReconciler(b *testing.B) {
	b.Skip("Skip benchmark for background routine")
}

func BenchmarkRunBridgeNode(b *testing.B) {
	b.Skip("Skip benchmark for background routine")
}

func TestPromptConfirmation(t *testing.T) {
	tests := []struct {
		name      string
		action    string
		force     bool
		input     string
		expectErr bool
	}{
		{"Force Skips", "install", true, "", false},
		{"Valid Input", "install", false, "install\n", false},
		{"Valid Input With Spaces", "install", false, " install \n", false},
		{"Invalid Input", "install", false, "yes\n", true},
		{"Empty Input", "install", false, "\n", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var out bytes.Buffer
			in := strings.NewReader(tt.input)
			err := promptConfirmation(tt.action, tt.force, in, &out)
			if tt.expectErr && err == nil {
				t.Errorf("expected error, got nil")
			}
			if !tt.expectErr && err != nil {
				t.Errorf("unexpected error: %v", err)
			}
		})
	}
}

func BenchmarkPromptConfirmation(b *testing.B) {
	b.Skip("Interactive function")
}
