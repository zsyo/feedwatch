//go:build webui

// Package secrets seals channel credentials with AES-256-GCM before they ever
// reach SQLite. It is the only place in the WebUI that knows about the master
// key, and the only way delivery can turn stored ciphertext back into a usable
// token.
//
// The key itself is never stored in the database: it lives in a 0600 file next
// to the database (so a volume backup carries both) or comes from the
// FEEDWATCH_WEBUI_SECRET_KEY environment variable. Losing it means every
// channel credential must be entered again, which is why loading never
// regenerates a key it failed to read.
package secrets

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
)

const (
	// EnvMasterKey optionally supplies the master key as standard base64 of
	// exactly KeySize bytes. When set it takes precedence over the key file.
	EnvMasterKey = "FEEDWATCH_WEBUI_SECRET_KEY"
	// FileName is the key file created inside the data directory.
	FileName = "secret.key"
	// KeySize is the AES-256 key length.
	KeySize = 32

	// envelopePrefix makes sealed values self-describing, so a future format
	// change is a clean error instead of a garbage decryption.
	envelopePrefix = "fwsec:v1:"
)

var (
	// ErrKey reports an unusable master key (wrong length, bad base64, unreadable file).
	ErrKey = errors.New("secrets: invalid master key")
	// ErrOpen reports a value that cannot be decrypted (wrong key, wrong AAD, tampering).
	ErrOpen = errors.New("secrets: cannot decrypt sealed value")
	// ErrPrefix reports an envelope written by an unknown version of this package.
	ErrPrefix = errors.New("secrets: unknown sealed envelope")
)

// Sealer encrypts and authenticates small string maps.
type Sealer struct {
	aead  cipher.AEAD
	keyID string
}

// New builds a Sealer from a raw 32-byte key. The key is never padded or
// truncated: a short or long key is a configuration bug, not something to guess.
func New(key []byte) (*Sealer, error) {
	if len(key) != KeySize {
		return nil, fmt.Errorf("%w: want %d bytes, got %d", ErrKey, KeySize, len(key))
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrKey, err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrKey, err)
	}
	sum := sha256.Sum256(key)
	return &Sealer{aead: aead, keyID: hex.EncodeToString(sum[:4])}, nil
}

// NewRandom returns a Sealer over a fresh random key. Callers that need to
// persist it should use LoadOrCreate instead.
func NewRandom() (*Sealer, error) {
	key := make([]byte, KeySize)
	if _, err := rand.Read(key); err != nil {
		return nil, fmt.Errorf("secrets: generate key: %w", err)
	}
	return New(key)
}

// KeyID is a short, non-reversible fingerprint used in logs to confirm that two
// installations share a key. It never leaves the server.
func (s *Sealer) KeyID() string { return s.keyID }

// Seal encrypts plain and returns "fwsec:v1:" + base64(nonce||ciphertext).
func (s *Sealer) Seal(plain, aad []byte) (string, error) {
	nonce := make([]byte, s.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", fmt.Errorf("secrets: generate nonce: %w", err)
	}
	// Seal appends to dst, so starting from the nonce yields nonce||ciphertext.
	out := s.aead.Seal(nonce, nonce, plain, aad)
	return envelopePrefix + base64.RawURLEncoding.EncodeToString(out), nil
}

// Open decrypts a value produced by Seal. Anything that is not a v1 envelope is
// rejected explicitly rather than parsed optimistically.
func (s *Sealer) Open(envelope string, aad []byte) ([]byte, error) {
	raw, ok := strings.CutPrefix(envelope, envelopePrefix)
	if !ok {
		return nil, ErrPrefix
	}
	buf, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil {
		return nil, fmt.Errorf("%w: bad base64", ErrOpen)
	}
	nonceSize := s.aead.NonceSize()
	if len(buf) < nonceSize+s.aead.Overhead() {
		return nil, fmt.Errorf("%w: truncated", ErrOpen)
	}
	plain, err := s.aead.Open(nil, buf[:nonceSize], buf[nonceSize:], aad)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrOpen, err)
	}
	return plain, nil
}

// SealMap encrypts a field->value map. An empty map seals to "" so callers can
// skip writing a row at all.
func (s *Sealer) SealMap(values map[string]string, aad []byte) (string, error) {
	if len(values) == 0 {
		return "", nil
	}
	plain, err := json.Marshal(values)
	if err != nil {
		return "", fmt.Errorf("secrets: encode fields: %w", err)
	}
	return s.Seal(plain, aad)
}

// OpenMap decrypts a map produced by SealMap. An empty envelope stays empty.
func (s *Sealer) OpenMap(envelope string, aad []byte) (map[string]string, error) {
	if strings.TrimSpace(envelope) == "" {
		return nil, nil
	}
	plain, err := s.Open(envelope, aad)
	if err != nil {
		return nil, err
	}
	trimmed := strings.TrimSpace(string(plain))
	if trimmed == "" || trimmed == "null" {
		return nil, nil
	}
	var out map[string]string
	if err := json.Unmarshal(plain, &out); err != nil {
		return nil, fmt.Errorf("%w: payload is not a string map", ErrOpen)
	}
	return out, nil
}

// ChannelAAD binds ciphertext to one channel row, so a copied envelope fails to
// decrypt under a different id instead of silently sending with someone else's
// credentials.
func ChannelAAD(id int64) []byte {
	return []byte("feedwatch:channel:" + fmt.Sprint(id))
}

// GithubTokenAAD binds the single, process-wide GitHub token to its purpose.
// Unlike channel secrets there is no row id: the token is shared by every
// GitHub subscription in this installation.
func GithubTokenAAD() []byte {
	return []byte("feedwatch:github-token")
}

// GithubTokenSettingKey is where the sealed token lives in the settings table.
// It is read at startup and must never be echoed back to the UI.
const GithubTokenSettingKey = "github.token"

// LoadOrCreate resolves the master key: environment variable first, then
// <dir>/secret.key, then a freshly generated file. Any read failure is fatal —
// regenerating would silently orphan every stored credential.
func LoadOrCreate(dir string, log *slog.Logger) (*Sealer, error) {
	if log == nil {
		log = slog.Default()
	}
	path, err := filepath.Abs(filepath.Join(dir, FileName))
	if err != nil {
		path = filepath.Join(dir, FileName)
	}

	if raw := strings.TrimSpace(os.Getenv(EnvMasterKey)); raw != "" {
		key, err := base64.StdEncoding.DecodeString(raw)
		if err != nil {
			return nil, fmt.Errorf("%w: %s is not valid base64: %w", ErrKey, EnvMasterKey, err)
		}
		sealer, err := New(key)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", EnvMasterKey, err)
		}
		if _, statErr := os.Stat(path); statErr == nil {
			log.Warn("webui: master key comes from the environment and overrides the key file; "+
				"stored channel credentials only decrypt with the same key",
				"key_source", "env", "key_id", sealer.KeyID(), "ignored_file", path)
		} else {
			log.Info("webui: master key loaded", "key_source", "env", "key_id", sealer.KeyID())
		}
		return sealer, nil
	}

	key, err := os.ReadFile(path)
	switch {
	case err == nil:
		if len(key) != KeySize {
			return nil, fmt.Errorf("%w: %s must hold exactly %d bytes, got %d; "+
				"restore it from backup, set %s to the original key, or delete the file "+
				"to accept that every channel credential has to be entered again",
				ErrKey, path, KeySize, len(key), EnvMasterKey)
		}
	case errors.Is(err, os.ErrNotExist):
		key, err = createKeyFile(path)
		if err != nil {
			return nil, err
		}
		log.Info("webui: master key created; back it up together with the database",
			"key_source", "file", "key_id", keyIDOf(key), "path", path)
		return New(key)
	default:
		return nil, fmt.Errorf("secrets: read %s: %w", path, err)
	}

	sealer, err := New(key)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	log.Info("webui: master key loaded", "key_source", "file", "key_id", sealer.KeyID(), "path", path)
	return sealer, nil
}

// createKeyFile writes a new 0600 key file. O_EXCL makes creation
// create-if-absent, so two processes starting at once cannot clobber each other:
// the loser simply reads the winner's file.
func createKeyFile(path string) ([]byte, error) {
	key := make([]byte, KeySize)
	if _, err := rand.Read(key); err != nil {
		return nil, fmt.Errorf("secrets: generate key: %w", err)
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if errors.Is(err, os.ErrExist) {
		existing, readErr := os.ReadFile(path)
		if readErr != nil {
			return nil, fmt.Errorf("secrets: read %s: %w", path, readErr)
		}
		if len(existing) != KeySize {
			return nil, fmt.Errorf("%w: %s must hold exactly %d bytes, got %d", ErrKey, path, KeySize, len(existing))
		}
		return existing, nil
	}
	if err != nil {
		return nil, fmt.Errorf("secrets: create %s: %w", path, err)
	}
	if _, err := f.Write(key); err != nil {
		f.Close()
		return nil, fmt.Errorf("secrets: write %s: %w", path, err)
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return nil, fmt.Errorf("secrets: sync %s: %w", path, err)
	}
	if err := f.Close(); err != nil {
		return nil, fmt.Errorf("secrets: close %s: %w", path, err)
	}
	return key, nil
}

func keyIDOf(key []byte) string {
	sum := sha256.Sum256(key)
	return hex.EncodeToString(sum[:4])
}
