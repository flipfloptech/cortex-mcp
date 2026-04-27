package vault

import (
	"crypto/ed25519"
	"crypto/rand"
	"testing"
)

// --- Vault creation ---

func TestNewVault_DerivedKey(t *testing.T) {
	t.Parallel()

	_, priv := generateTestKey(t)
	v, err := New(priv)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	if v == nil {
		t.Fatal("vault should not be nil")
	}
}

func TestNewVault_NilKey_Error(t *testing.T) {
	t.Parallel()

	_, err := New(nil)
	if err == nil {
		t.Fatal("expected error for nil key")
	}
}

func BenchmarkNew(b *testing.B) {
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		b.Fatalf("generate key: %v", err)
	}

	b.ReportAllocs()
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		v, err := New(priv)
		if err != nil {
			b.Fatalf("New: %v", err)
		}
		_ = v
	}
}

// --- Store and retrieve credentials ---

func TestVault_StoreAndGet_SSHKey(t *testing.T) {
	t.Parallel()

	_, priv := generateTestKey(t)
	v, _ := New(priv)

	cred := Credential{
		Type:       CredSSHKey,
		Username:   "deploy",
		PrivateKey: []byte("-----BEGIN OPENSSH PRIVATE KEY-----\nfake-key\n-----END OPENSSH PRIVATE KEY-----"),
	}

	v.Store("10.0.1.*", cred)

	got, ok := v.Match("10.0.1.5")
	if !ok {
		t.Fatal("expected match for 10.0.1.5")
	}
	if got.Username != "deploy" {
		t.Fatalf("username = %q, want %q", got.Username, "deploy")
	}
	if got.Type != CredSSHKey {
		t.Fatalf("type = %d, want %d", got.Type, CredSSHKey)
	}
}

func TestVault_StoreAndGet_SSHPassword(t *testing.T) {
	t.Parallel()

	_, priv := generateTestKey(t)
	v, _ := New(priv)

	cred := Credential{
		Type:     CredSSHPassword,
		Username: "root",
		Password: "s3cret",
	}

	v.Store("*.internal.corp", cred)

	got, ok := v.Match("mds-01.internal.corp")
	if !ok {
		t.Fatal("expected match for mds-01.internal.corp")
	}
	if got.Password != "s3cret" {
		t.Fatalf("password = %q, want %q", got.Password, "s3cret")
	}
}

func TestVault_StoreAndGet_TLSCert(t *testing.T) {
	t.Parallel()

	_, priv := generateTestKey(t)
	v, _ := New(priv)

	cred := Credential{
		Type:        CredTLSCert,
		Certificate: []byte("fake-cert"),
		PrivateKey:  []byte("fake-key"),
	}

	v.Store("gateway.*", cred)

	got, ok := v.Match("gateway.prod")
	if !ok {
		t.Fatal("expected match for gateway.prod")
	}
	if got.Type != CredTLSCert {
		t.Fatalf("type = %d, want %d", got.Type, CredTLSCert)
	}
}

func TestVault_Match_NoMatch(t *testing.T) {
	t.Parallel()

	_, priv := generateTestKey(t)
	v, _ := New(priv)

	v.Store("10.0.1.*", Credential{Type: CredSSHKey, Username: "deploy"})

	if _, ok := v.Match("10.0.2.5"); ok {
		t.Fatal("should not match 10.0.2.5 against 10.0.1.*")
	}
}

func TestVault_Match_ExactHost(t *testing.T) {
	t.Parallel()

	_, priv := generateTestKey(t)
	v, _ := New(priv)

	v.Store("mds-01", Credential{Type: CredSSHKey, Username: "root"})

	got, ok := v.Match("mds-01")
	if !ok {
		t.Fatal("expected exact match")
	}
	if got.Username != "root" {
		t.Fatalf("username = %q, want %q", got.Username, "root")
	}
}

func TestVault_Match_MostSpecificWins(t *testing.T) {
	t.Parallel()

	_, priv := generateTestKey(t)
	v, _ := New(priv)

	// Register broad then specific.
	v.Store("*", Credential{Type: CredSSHKey, Username: "broad"})
	v.Store("10.0.1.*", Credential{Type: CredSSHKey, Username: "subnet"})
	v.Store("10.0.1.5", Credential{Type: CredSSHKey, Username: "exact"})

	got, ok := v.Match("10.0.1.5")
	if !ok {
		t.Fatal("expected match")
	}
	if got.Username != "exact" {
		t.Fatalf("username = %q, want %q (most specific should win)", got.Username, "exact")
	}
}

func TestVault_Store_Overwrite(t *testing.T) {
	t.Parallel()

	_, priv := generateTestKey(t)
	v, _ := New(priv)

	v.Store("10.0.1.*", Credential{Type: CredSSHKey, Username: "old"})
	v.Store("10.0.1.*", Credential{Type: CredSSHKey, Username: "new"})

	got, ok := v.Match("10.0.1.5")
	if !ok {
		t.Fatal("expected match")
	}
	if got.Username != "new" {
		t.Fatalf("username = %q, want %q", got.Username, "new")
	}
}

func BenchmarkStore(b *testing.B) {
	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	v, _ := New(priv)

	cred := Credential{Type: CredSSHKey, Username: "bench"}
	pattern := "10.0.1.*"

	b.ReportAllocs()
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		v.Store(pattern, cred)
	}
}

// --- Seal/Unseal (encrypt at rest) ---

func TestVault_SealUnseal_Roundtrip(t *testing.T) {
	t.Parallel()

	_, priv := generateTestKey(t)
	v, _ := New(priv)

	v.Store("10.0.1.*", Credential{Type: CredSSHKey, Username: "deploy", PrivateKey: []byte("my-key")})
	v.Store("*.prod", Credential{Type: CredSSHPassword, Username: "root", Password: "s3cret"})

	// Seal to bytes.
	sealed, err := v.Seal()
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}

	if len(sealed) == 0 {
		t.Fatal("sealed should not be empty")
	}

	// Unseal into a new vault with the same key.
	v2, err := Unseal(priv, sealed)
	if err != nil {
		t.Fatalf("Unseal: %v", err)
	}

	// Verify credentials survived.
	got, ok := v2.Match("10.0.1.5")
	if !ok {
		t.Fatal("expected match after unseal")
	}
	if got.Username != "deploy" {
		t.Fatalf("username = %q, want %q", got.Username, "deploy")
	}

	got2, ok := v2.Match("mds.prod")
	if !ok {
		t.Fatal("expected match after unseal")
	}
	if got2.Password != "s3cret" {
		t.Fatalf("password = %q, want %q", got2.Password, "s3cret")
	}
}

func TestVault_Unseal_WrongKey(t *testing.T) {
	t.Parallel()

	_, priv1 := generateTestKey(t)
	_, priv2 := generateTestKey(t)

	v, _ := New(priv1)
	v.Store("host", Credential{Type: CredSSHKey, Username: "user"})

	sealed, _ := v.Seal()

	// Unseal with a different key should fail.
	_, err := Unseal(priv2, sealed)
	if err == nil {
		t.Fatal("expected error when unsealing with wrong key")
	}
}

func TestVault_Unseal_CorruptedData(t *testing.T) {
	t.Parallel()

	_, priv := generateTestKey(t)
	v, _ := New(priv)
	v.Store("host", Credential{Type: CredSSHKey, Username: "user"})

	sealed, _ := v.Seal()

	// Corrupt the sealed data.
	sealed[len(sealed)/2] ^= 0xFF

	_, err := Unseal(priv, sealed)
	if err == nil {
		t.Fatal("expected error for corrupted data")
	}
}

func TestVault_Seal_Empty(t *testing.T) {
	t.Parallel()

	_, priv := generateTestKey(t)
	v, _ := New(priv)

	sealed, err := v.Seal()
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}

	v2, err := Unseal(priv, sealed)
	if err != nil {
		t.Fatalf("Unseal: %v", err)
	}

	if _, ok := v2.Match("anything"); ok {
		t.Fatal("empty vault should not match anything")
	}
}

func BenchmarkSeal(b *testing.B) {
	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	v, _ := New(priv)

	v.Store("10.0.1.*", Credential{Type: CredSSHKey, Username: "deploy", PrivateKey: []byte("my-key")})
	v.Store("*.prod", Credential{Type: CredSSHPassword, Username: "root", Password: "s3cret"})

	b.ReportAllocs()
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		_, err := v.Seal()
		if err != nil {
			b.Fatalf("Seal: %v", err)
		}
	}
}

func BenchmarkUnseal(b *testing.B) {
	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	v, _ := New(priv)

	v.Store("10.0.1.*", Credential{Type: CredSSHKey, Username: "deploy", PrivateKey: []byte("my-key")})
	v.Store("*.prod", Credential{Type: CredSSHPassword, Username: "root", Password: "s3cret"})

	sealed, _ := v.Seal()

	b.ReportAllocs()
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		_, err := Unseal(priv, sealed)
		if err != nil {
			b.Fatalf("Unseal: %v", err)
		}
	}
}

// --- AllPatterns ---

func TestVault_AllPatterns(t *testing.T) {
	t.Parallel()

	_, priv := generateTestKey(t)
	v, _ := New(priv)

	v.Store("10.0.1.*", Credential{Type: CredSSHKey, Username: "a"})
	v.Store("*.prod", Credential{Type: CredSSHKey, Username: "b"})

	patterns := v.AllPatterns()
	if len(patterns) != 2 {
		t.Fatalf("patterns = %d, want 2", len(patterns))
	}
}

func BenchmarkAllPatterns(b *testing.B) {
	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	v, _ := New(priv)

	v.Store("10.0.1.*", Credential{Type: CredSSHKey, Username: "a"})
	v.Store("*.prod", Credential{Type: CredSSHKey, Username: "b"})
	v.Store("node[1-100]", Credential{Type: CredSSHKey, Username: "c"})

	b.ReportAllocs()
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		_ = v.AllPatterns()
	}
}

// --- Concurrent access ---

func TestVault_ConcurrentStoreAndMatch(t *testing.T) {
	t.Parallel()

	_, priv := generateTestKey(t)
	v, _ := New(priv)

	done := make(chan struct{})
	go func() {
		for i := 0; i < 100; i++ {
			v.Store("10.0.1.*", Credential{Type: CredSSHKey, Username: "user"})
		}
		close(done)
	}()

	for i := 0; i < 100; i++ {
		v.Match("10.0.1.5")
	}
	<-done
}

// --- Nodeset pattern matching ---

func TestVault_MatchNodesetRange(t *testing.T) {
	t.Parallel()

	_, priv := generateTestKey(t)
	v, _ := New(priv)

	v.Store("node[1-100]", Credential{Type: CredSSHKey, Username: "range"})

	cred, ok := v.Match("node50")
	if !ok {
		t.Fatal("expected match for node50 against node[1-100]")
	}
	if cred.Username != "range" {
		t.Fatalf("Username = %q, want %q", cred.Username, "range")
	}

	// Should not match outside range.
	_, ok = v.Match("node101")
	if ok {
		t.Fatal("node101 should not match node[1-100]")
	}
}

func TestVault_MatchNodesetIPRange(t *testing.T) {
	t.Parallel()

	_, priv := generateTestKey(t)
	v, _ := New(priv)

	v.Store("10.0.1.[1-50]", Credential{Type: CredSSHKey, Username: "subnet"})

	cred, ok := v.Match("10.0.1.25")
	if !ok {
		t.Fatal("expected match for 10.0.1.25")
	}
	if cred.Username != "subnet" {
		t.Fatalf("Username = %q, want %q", cred.Username, "subnet")
	}

	_, ok = v.Match("10.0.1.51")
	if ok {
		t.Fatal("10.0.1.51 should not match 10.0.1.[1-50]")
	}
}

func TestVault_MatchNodesetCommaList(t *testing.T) {
	t.Parallel()

	_, priv := generateTestKey(t)
	v, _ := New(priv)

	v.Store("memp-aqr-mvm0[0-3],memp-aqs-oss24", Credential{
		Type:     CredSSHKey,
		Username: "combo",
	})

	cred, ok := v.Match("memp-aqr-mvm02")
	if !ok {
		t.Fatal("expected match for memp-aqr-mvm02")
	}
	if cred.Username != "combo" {
		t.Fatalf("Username = %q, want %q", cred.Username, "combo")
	}

	cred, ok = v.Match("memp-aqs-oss24")
	if !ok {
		t.Fatal("expected match for memp-aqs-oss24")
	}
	if cred.Username != "combo" {
		t.Fatalf("Username = %q, want %q", cred.Username, "combo")
	}

	_, ok = v.Match("memp-aqr-mvm05")
	if ok {
		t.Fatal("memp-aqr-mvm05 should not match")
	}
}

func TestVault_MatchNodesetSpecificity(t *testing.T) {
	t.Parallel()

	_, priv := generateTestKey(t)
	v, _ := New(priv)

	// Three patterns with different specificity:
	// glob < nodeset range < exact
	v.Store("node*", Credential{Type: CredSSHKey, Username: "glob"})
	v.Store("node[1-100]", Credential{Type: CredSSHKey, Username: "range"})
	v.Store("node50", Credential{Type: CredSSHKey, Username: "exact"})

	// Exact should win for node50.
	cred, ok := v.Match("node50")
	if !ok {
		t.Fatal("expected match for node50")
	}
	if cred.Username != "exact" {
		t.Fatalf("Username = %q, want %q (exact should win)", cred.Username, "exact")
	}

	// Range should win for node25 (no exact match).
	cred, ok = v.Match("node25")
	if !ok {
		t.Fatal("expected match for node25")
	}
	if cred.Username != "range" {
		t.Fatalf("Username = %q, want %q (range should win over glob)", cred.Username, "range")
	}
}

func BenchmarkMatch(b *testing.B) {
	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	v, _ := New(priv)

	v.Store("node*", Credential{Type: CredSSHKey, Username: "glob"})
	v.Store("node[1-100]", Credential{Type: CredSSHKey, Username: "range"})
	v.Store("node50", Credential{Type: CredSSHKey, Username: "exact"})

	b.ReportAllocs()
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		v.Match("node50")
	}
}

// --- Helpers ---

func generateTestKey(t *testing.T) (ed25519.PublicKey, ed25519.PrivateKey) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	return pub, priv
}
