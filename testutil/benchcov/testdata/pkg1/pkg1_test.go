package pkg1

import "testing"

func BenchmarkExportedFunc1(b *testing.B)   {}
func BenchmarkUnexportedFunc2(b *testing.B) {}
