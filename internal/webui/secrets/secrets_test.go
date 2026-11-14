//go:build webui

package secrets

import (
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func mustSealer(t *testing.T) *Sealer {
	t.Helper()
	s, err := NewRandom()
	if err != nil {
		t.Fatalf("NewRandom: %v", err)
	}
	return s
}

func TestSealOpenRoundTrip(t *testing.T) {
	s := mustSealer(t)
	aad := ChannelAAD(42)
	envelope, err := s.Seal([]byte("re_123456"), aad)
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	if !strings.HasPrefix(envelope, envelopePrefix) {
		t.Fatalf("envelope %q lacks the version prefix", envelope)
	}
	plain, err := s.Open(envelope, aad)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if string(plain) != "re_123456" {
		t.Fatalf("round trip = %q", plain)
	}
	// A second Seal of the same plaintext must differ (fresh nonce).
	again, err := s.Seal([]byte("re_123456"), aad)
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	if again == envelope {
		t.Fatal("two seals produced the same ciphertext; nonce is not random")
	}
}

func TestOpenRejectsTamperedEnvelope(t *testing.T) {
	s := mustSealer(t)
	envelope, err := s.Seal([]byte("token"), ChannelAAD(1))
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(envelope, envelopePrefix))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	raw[len(raw)-1] ^= 0xff
	tampered := envelopePrefix + base64.RawURLEncoding.EncodeToString(raw)
	if _, err := s.Open(tampered, ChannelAAD(1)); !errors.Is(err, ErrOpen) {
		t.Fatalf("Open(tampered) error = %v, want ErrOpen", err)
	}
}

func TestOpenRejectsWrongAAD(t *testing.T) {
	s := mustSealer(t)
	envelope, err := s.Seal([]byte("token"), ChannelAAD(1))
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	if _, err := s.Open(envelope, ChannelAAD(2)); !errors.Is(err, ErrOpen) {
		t.Fatalf("Open(wrong aad) error = %v, want ErrOpen", err)
	}
}

func TestOpenRejectsUnknownPrefix(t *testing.T) {
	s := mustSealer(t)
	for _, envelope := range []string{"", "re_plaintext", "fwsec:v2:AAAA", strings.TrimPrefix("fwsec:v1:abc", "fwsec:v1:")} {
		if _, err := s.Open(envelope, nil); !errors.Is(err, ErrPrefix) {
			t.Fatalf("Open(%q) error = %v, want ErrPrefix", envelope, err)
		}
	}
}

func TestNewRejectsWrongKeyLength(t *testing.T) {
	for _, n := range []int{0, 16, KeySize - 1, KeySize + 1} {
		if _, err := New(make([]byte, n)); !errors.Is(err, ErrKey) {
			t.Fatalf("New(%d bytes) error = %v, want ErrKey", n, err)
		}
	}
}

func TestSealMapEmptyAndRoundTrip(t *testing.T) {
	s := mustSealer(t)
	envelope, err := s.SealMap(nil, ChannelAAD(7))
	if err != nil {
		t.Fatalf("SealMap(nil): %v", err)
	}
	if envelope != "" {
		t.Fatalf("SealMap(nil) = %q, want empty", envelope)
	}
	in := map[string]string{"api_key": "re_1", "secret": "abc"}
	envelope, err = s.SealMap(in, ChannelAAD(7))
	if err != nil {
		t.Fatalf("SealMap: %v", err)
	}
	out, err := s.OpenMap(envelope, ChannelAAD(7))
	if err != nil {
		t.Fatalf("OpenMap: %v", err)
	}
	if len(out) != 2 || out["api_key"] != "re_1" || out["secret"] != "abc" {
		t.Fatalf("OpenMap = %#v", out)
	}
	if got, err := s.OpenMap("", nil); err != nil || got != nil {
		t.Fatalf("OpenMap(empty) = %#v, %v", got, err)
	}
}

func TestLoadOrCreateCreatesThenReloads(t *testing.T) {
	t.Setenv(EnvMasterKey, "")
	dir := t.TempDir()
	first, err := LoadOrCreate(dir, nil)
	if err != nil {
		t.Fatalf("LoadOrCreate: %v", err)
	}
	path := filepath.Join(dir, FileName)
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat key file: %v", err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm()&0o077 != 0 {
		t.Fatalf("key file mode = %v, want 0600", info.Mode().Perm())
	}
	second, err := LoadOrCreate(dir, nil)
	if err != nil {
		t.Fatalf("second LoadOrCreate: %v", err)
	}
	if second.KeyID() != first.KeyID() {
		t.Fatalf("reloaded key id %q != %q", second.KeyID(), first.KeyID())
	}
}

func TestLoadOrCreateDoesNotOverwriteBrokenFile(t *testing.T) {
	t.Setenv(EnvMasterKey, "")
	dir := t.TempDir()
	path := filepath.Join(dir, FileName)
	broken := []byte("too-short")
	if err := os.WriteFile(path, broken, 0o600); err != nil {
		t.Fatalf("write broken key: %v", err)
	}
	if _, err := LoadOrCreate(dir, nil); !errors.Is(err, ErrKey) {
		t.Fatalf("LoadOrCreate error = %v, want ErrKey", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if string(got) != string(broken) {
		t.Fatalf("broken key file was rewritten: %q", got)
	}
}

func TestLoadOrCreatePrefersEnvironment(t *testing.T) {
	dir := t.TempDir()
	fileSealer, err := LoadOrCreate(dir, nil)
	if err != nil {
		t.Fatalf("LoadOrCreate: %v", err)
	}
	// A different key supplied through the environment must win over the file.
	envKey := make([]byte, KeySize)
	for i := range envKey {
		envKey[i] = byte(i + 1)
	}
	fromEnv, err := New(envKey)
	if err != nil {
		t.Fatalf("New(env key): %v", err)
	}
	t.Setenv(EnvMasterKey, base64.StdEncoding.EncodeToString(envKey))
	loaded, err := LoadOrCreate(dir, nil)
	if err != nil {
		t.Fatalf("LoadOrCreate with env: %v", err)
	}
	if loaded.KeyID() != fromEnv.KeyID() {
		t.Fatalf("loaded key id %q != env key id %q", loaded.KeyID(), fromEnv.KeyID())
	}
	envelope, err := fromEnv.Seal([]byte("x"), nil)
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	if _, err := fileSealer.Open(envelope, nil); !errors.Is(err, ErrOpen) {
		t.Fatalf("file key decrypted an env-key envelope: %v", err)
	}
	if _, err := loaded.Open(envelope, nil); err != nil {
		t.Fatalf("loaded sealer cannot open env envelope: %v", err)
	}
}

func TestLoadOrCreateRejectsBadEnvKey(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(EnvMasterKey, "not base64!!")
	if _, err := LoadOrCreate(dir, nil); !errors.Is(err, ErrKey) {
		t.Fatalf("LoadOrCreate error = %v, want ErrKey", err)
	}
	t.Setenv(EnvMasterKey, base64.StdEncoding.EncodeToString([]byte("short")))
	if _, err := LoadOrCreate(dir, nil); !errors.Is(err, ErrKey) {
		t.Fatalf("LoadOrCreate(short) error = %v, want ErrKey", err)
	}
	if _, err := os.Stat(filepath.Join(dir, FileName)); !os.IsNotExist(err) {
		t.Fatal("a failed env key must not create a key file")
	}
}
