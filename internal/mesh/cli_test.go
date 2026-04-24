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
		input     string
		expectErr bool
	}{
		{"Valid Input", "install", "install\n", false},
		{"Valid Input With Spaces", "install", " install \n", false},
		{"Invalid Input", "install", "yes\n", true},
		{"Empty Input", "install", "\n", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var out bytes.Buffer
			in := strings.NewReader(tt.input)
			err := promptConfirmation(tt.action, in, &out)
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
