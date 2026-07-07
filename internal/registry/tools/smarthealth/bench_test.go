package smarthealth

import (
	"context"
	"testing"
)

func BenchmarkNew(b *testing.B) {
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = New()
	}
}

func BenchmarkName(b *testing.B) {
	tool := New()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = tool.Name()
	}
}

func BenchmarkDescription(b *testing.B) {
	tool := New()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = tool.Description()
	}
}

func BenchmarkHelp(b *testing.B) {
	tool := New()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = tool.Help()
	}
}

func BenchmarkCategory(b *testing.B) {
	tool := New()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = tool.Category()
	}
}

func BenchmarkParameters(b *testing.B) {
	tool := New()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = tool.Parameters()
	}
}

func BenchmarkHidden(b *testing.B) {
	tool := New()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = tool.Hidden()
	}
}

func BenchmarkIsSupported(b *testing.B) {
	tool := newFixtureTool(b)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = tool.IsSupported()
	}
}

func BenchmarkExecute(b *testing.B) {
	tool := newFixtureTool(b)
	ctx := context.Background()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = tool.Execute(ctx, nil)
	}
}

func BenchmarkDiscoverSATADrives(b *testing.B) {
	tool := newFixtureTool(b)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = discoverSATADrives(tool.sysfsRoot)
	}
}

func BenchmarkRunSmartctl(b *testing.B) {
	tool := newFixtureTool(b)
	ctx := context.Background()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = tool.runSmartctl(ctx, "sda")
	}
}

func BenchmarkParseSmartctl(b *testing.B) {
	data := []byte(healthySmartctlJSON)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = parseSmartctl(data, "sda", "FALLBACK")
	}
}

func BenchmarkApplyHealthRules(b *testing.B) {
	d, err := parseSmartctl([]byte(failingSmartctlJSON), "sda", "")
	if err != nil {
		b.Fatal(err)
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		drive := d
		applyHealthRules(&drive)
	}
}

func BenchmarkIsPermissionDenied(b *testing.B) {
	out := []byte("sudo: a password is required")
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = isPermissionDenied(nil, out)
	}
}
