package memory

import (
	"bytes"
	"context"
	"testing"
)

func TestParseBuddyInfo(t *testing.T) {
	data := []byte(`Node 0, zone      DMA      0      1      1      0      1      0      0      1      0      1      2 
Node 0, zone    DMA32    170     47    117    155    169    140    105    109     96     66     27 
Node 0, zone   Normal  20602  34841 484680 387558 175939  51271  10080   1889    166    422     56 
`)

	res, err := ParseBuddyInfo(context.Background(), bytes.NewReader(data))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(res.Zones) != 3 {
		t.Errorf("expected 3 zones, got %d", len(res.Zones))
	}

	if res.SystemSummary.TotalFreeMB == 0 {
		t.Errorf("expected >0 total free MB, got %d", res.SystemSummary.TotalFreeMB)
	}

	if res.SystemSummary.FragmentationScore < 0 || res.SystemSummary.FragmentationScore > 100 {
		t.Errorf("expected fragmentation score between 0 and 100, got %d", res.SystemSummary.FragmentationScore)
	}
}

func TestGetBuddyInfo(t *testing.T) {
	if !IsBuddyInfoSupported() {
		t.Skip("buddyinfo not supported")
	}

	res, err := GetBuddyInfo(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(res.Zones) == 0 {
		t.Errorf("expected >0 zones, got %d", len(res.Zones))
	}
}

func BenchmarkParseBuddyInfo(b *testing.B) {
	data := []byte(`Node 0, zone      DMA      0      1      1      0      1      0      0      1      0      1      2 
Node 0, zone    DMA32    170     47    117    155    169    140    105    109     96     66     27 
Node 0, zone   Normal  20602  34841 484680 387558 175939  51271  10080   1889    166    422     56 
`)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = ParseBuddyInfo(context.Background(), bytes.NewReader(data))
	}
}

func BenchmarkIsBuddyInfoSupported(b *testing.B) {
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = IsBuddyInfoSupported()
	}
}

func BenchmarkGetBuddyInfo(b *testing.B) {
	if !IsBuddyInfoSupported() {
		b.Skip("buddyinfo not supported")
	}
	ctx := context.Background()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = GetBuddyInfo(ctx)
	}
}
