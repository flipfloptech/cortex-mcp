package membrane

import (
	"context"
	"net"
	"testing"
)

func BenchmarkMeshConfig(b *testing.B) {
	b.ReportAllocs()
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		_ = MeshConfig()
	}
}

func BenchmarkMultiplex(b *testing.B) {
	b.ReportAllocs()
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		b.StopTimer()
		cRaw, sRaw := net.Pipe()
		b.StartTimer()

		go func() {
			sMC, _ := Multiplex(sRaw, true)
			if sMC != nil {
				_ = sMC.Close()
			}
		}()

		cMC, _ := Multiplex(cRaw, false)
		if cMC != nil {
			_ = cMC.Close()
		}
	}
}

func BenchmarkMultiplexDual(b *testing.B) {
	b.ReportAllocs()
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		b.StopTimer()
		c1, s1 := net.Pipe()
		c2, s2 := net.Pipe()
		b.StartTimer()

		go func() {
			sMC, _ := MultiplexDual(s1, s2, true)
			if sMC != nil {
				_ = sMC.Close()
			}
		}()

		cMC, _ := MultiplexDual(c1, c2, false)
		if cMC != nil {
			_ = cMC.Close()
		}
	}
}

func BenchmarkUpgrade(b *testing.B) {
	serverCfg, clientCfg := testMembranePair(b)

	b.ReportAllocs()
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		b.StopTimer()
		clientRaw, serverRaw := net.Pipe()
		go func() {
			mc, _ := Upgrade(context.Background(), clientRaw, clientCfg, false)
			if mc != nil {
				_ = clientRaw.Close()
				_ = mc.Close()
			}
		}()
		b.StartTimer()

		mc, err := Upgrade(context.Background(), serverRaw, serverCfg, true)
		if err == nil {
			_ = serverRaw.Close()
			_ = mc.Close()
		}
	}
}

func BenchmarkUpgradeDual(b *testing.B) {
	serverCfg, clientCfg := testMembranePair(b)

	b.ReportAllocs()
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		b.StopTimer()
		cRaw1, sRaw1 := net.Pipe()
		cRaw2, sRaw2 := net.Pipe()

		go func() {
			mc, _ := UpgradeDual(context.Background(), cRaw1, cRaw2, clientCfg, false)
			if mc != nil {
				_ = cRaw1.Close()
				_ = cRaw2.Close()
				_ = mc.Close()
			}
		}()
		b.StartTimer()

		mc, err := UpgradeDual(context.Background(), sRaw1, sRaw2, serverCfg, true)
		if err == nil {
			_ = sRaw1.Close()
			_ = sRaw2.Close()
			_ = mc.Close()
		}
	}
}

func BenchmarkClose(b *testing.B) {
	b.ReportAllocs()
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		b.StopTimer()
		cRaw, sRaw := net.Pipe()

		go func() {
			cMC, _ := Multiplex(cRaw, false)
			if cMC != nil {
				_ = cMC.Close()
			}
		}()

		sMC, _ := Multiplex(sRaw, true)
		b.StartTimer()

		if sMC != nil {
			_ = sMC.Close()
		}
	}
}
