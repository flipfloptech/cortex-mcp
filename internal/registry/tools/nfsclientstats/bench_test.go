package nfsclientstats

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
	t := New()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = t.Name()
	}
}

func BenchmarkDescription(b *testing.B) {
	t := New()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = t.Description()
	}
}

func BenchmarkHelp(b *testing.B) {
	t := New()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = t.Help()
	}
}

func BenchmarkCategory(b *testing.B) {
	t := New()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = t.Category()
	}
}

func BenchmarkParameters(b *testing.B) {
	t := New()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = t.Parameters()
	}
}

func BenchmarkHidden(b *testing.B) {
	t := New()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = t.Hidden()
	}
}

func BenchmarkIsSupported(b *testing.B) {
	t := New()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = t.IsSupported()
	}
}

func BenchmarkExecute(b *testing.B) {
	t := New()
	ctx := context.Background()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = t.Execute(ctx, nil)
	}
}

func BenchmarkParseNFSServers(b *testing.B) {
	content := []byte(`NV SERVER   PORT USE HOSTNAME
v4 c0a8010a  801   1 192.168.1.10
`)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = parseNFSServers(content)
	}
}

func BenchmarkParseNFSVolumes(b *testing.B) {
	content := []byte(`NV SERVER   PORT    DEV     FSID    FSC
v4 86826879 801     0:46    0:0     no
`)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = parseNFSVolumes(content)
	}
}

func BenchmarkParseNFSMountstats(b *testing.B) {
	content := []byte(`device nfs-server:/export/share mounted on /mnt/nfs with fstype nfs4 statvers=1.1
	opts:	rw,vers=4.2,rsize=1048576,wsize=1048576
	age:	123456
	xprt:	tcp 12345 0 1 0 0 1000 995 2 50 100 32 3
`)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = parseNFSMountstats(content)
	}
}
