package hosting

import (
	"errors"
	"sync"

	"github.com/zalando/go-keyring"
)

// Secrets is where tokens are kept, one per server. Not the database: that is
// a file beside the data, read with any SQLite browser and copied along with
// it, and a token is neither.
type Secrets interface {
	// Token is the one kept for a server, or empty when there is none.
	Token(server string) (string, error)
	SetToken(server, token string) error
	DeleteToken(server string) error
}

// keyringService is the name the system keychain shows the entries under.
const keyringService = "XXVI"

// Keyring is the system's own: Keychain on macOS, the Credential Manager on
// Windows, the Secret Service on Linux.
type Keyring struct{}

func (Keyring) Token(server string) (string, error) {
	token, err := keyring.Get(keyringService, server)
	if errors.Is(err, keyring.ErrNotFound) {
		return "", nil
	}
	return token, err
}

func (Keyring) SetToken(server, token string) error {
	return keyring.Set(keyringService, server, token)
}

func (Keyring) DeleteToken(server string) error {
	err := keyring.Delete(keyringService, server)
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

func (m *Memory) Token(server string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.tokens[server], nil
}

func (m *Memory) SetToken(server, token string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.tokens == nil {
		m.tokens = map[string]string{}
	}
	m.tokens[server] = token
	return nil
}

func (m *Memory) DeleteToken(server string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.tokens, server)
	return nil
}
