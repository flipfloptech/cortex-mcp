package registry

import (
	"encoding/json"
	"testing"
)

func BenchmarkString(b *testing.B) {
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_ = CategoryStorage.String()
	}
}

func BenchmarkParseCategory(b *testing.B) {
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_, _ = ParseCategory("storage")
	}
}

func BenchmarkParseCategoryMixedCase(b *testing.B) {
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_, _ = ParseCategory("Storage")
	}
}

func BenchmarkAllCategories(b *testing.B) {
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_ = AllCategories()
	}
}

func BenchmarkCategoryNames(b *testing.B) {
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_ = CategoryNames()
	}
}

func BenchmarkMarshalJSON(b *testing.B) {
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_, _ = json.Marshal(CategoryStorage)
	}
}

func BenchmarkUnmarshalJSON(b *testing.B) {
	data := []byte(`"storage"`)
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		var c Category
		_ = json.Unmarshal(data, &c)
	}
}
