package secrets

import (
	"errors"
	"sync"

	"github.com/zalando/go-keyring"
)

// Store is where tokens are kept: one per hosting server, one per source. Not
// the database: that is a file beside the data, read with any SQLite browser
// and copied along with it, and a token is neither.
type Store interface {
	// Token is the one kept under a key, or empty when there is none.
	Token(key string) (string, error)
	SetToken(key, token string) error
	DeleteToken(key string) error
}

// keyringService is the name the system keychain shows the entries under.
const keyringService = "XXVI"

// Keyring is the system's own: Keychain on macOS, the Credential Manager on
// Windows, the Secret Service on Linux.
type Keyring struct{}

func (Keyring) Token(key string) (string, error) {
	token, err := keyring.Get(keyringService, key)
	if errors.Is(err, keyring.ErrNotFound) {
		return "", nil
	}
	return token, err
}

func (Keyring) SetToken(key, token string) error {
	return keyring.Set(keyringService, key, token)
}

func (Keyring) DeleteToken(key string) error {
	err := keyring.Delete(keyringService, key)
	if errors.Is(err, keyring.ErrNotFound) {
		return nil
	}
	return err
}

// Memory keeps tokens for as long as the process lives: for tests, and for a
// machine whose keychain cannot be reached.
type Memory struct {
	mu     sync.Mutex
	tokens map[string]string
}

func (m *Memory) Token(key string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.tokens[key], nil
}

func (m *Memory) SetToken(key, token string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.tokens == nil {
		m.tokens = map[string]string{}
	}
	m.tokens[key] = token
	return nil
}

func (m *Memory) DeleteToken(key string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.tokens, key)
	return nil
}
