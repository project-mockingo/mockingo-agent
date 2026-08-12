package oauth

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/project-mockingo/mockingo-agent/internal/atomicfile"
	keyring "github.com/zalando/go-keyring"
)

const CredentialService = "mockingo"

var ErrCredentialsNotFound = errors.New("OAuth credentials not found")

type OAuthCredentials struct {
	AccessToken  string    `json:"accessToken"`
	RefreshToken string    `json:"refreshToken"`
	TokenType    string    `json:"tokenType"`
	Scope        []string  `json:"scope"`
	ExpiresAt    time.Time `json:"expiresAt"`
	UserID       string    `json:"userId"`
}

func Account(issuer, clientID string) string {
	return "oauth:" + strings.TrimRight(issuer, "/") + ":" + clientID
}

type CredentialStore interface {
	Get(account string) (OAuthCredentials, error)
	Set(account string, credentials OAuthCredentials) error
	Delete(account string) error
}

type KeyringStore struct{}

func encodeCredentials(credentials OAuthCredentials) (string, error) {
	data, err := json.Marshal(credentials)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

func decodeCredentials(value string) (OAuthCredentials, error) {
	var credentials OAuthCredentials
	if err := json.Unmarshal([]byte(value), &credentials); err != nil {
		return OAuthCredentials{}, fmt.Errorf("decode OAuth credentials: %w", err)
	}
	if credentials.AccessToken == "" || credentials.RefreshToken == "" || !strings.EqualFold(credentials.TokenType, "Bearer") || credentials.ExpiresAt.IsZero() {
		return OAuthCredentials{}, errors.New("stored OAuth credentials are incomplete")
	}
	return credentials, nil
}

func (KeyringStore) Get(account string) (OAuthCredentials, error) {
	value, err := keyring.Get(CredentialService, account)
	if errors.Is(err, keyring.ErrNotFound) {
		return OAuthCredentials{}, ErrCredentialsNotFound
	}
	if err != nil {
		return OAuthCredentials{}, fmt.Errorf("read operating-system credential store: %w", err)
	}
	return decodeCredentials(value)
}

func (KeyringStore) Set(account string, credentials OAuthCredentials) error {
	value, err := encodeCredentials(credentials)
	if err != nil {
		return err
	}
	if err := keyring.Set(CredentialService, account, value); err != nil {
		return fmt.Errorf("write operating-system credential store: %w", err)
	}
	return nil
}

func (KeyringStore) Delete(account string) error {
	err := keyring.Delete(CredentialService, account)
	if errors.Is(err, keyring.ErrNotFound) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("delete operating-system credential: %w", err)
	}
	return nil
}

type FileStore struct{ Directory string }

func (s FileStore) path(account string) string {
	digest := sha256.Sum256([]byte(account))
	return filepath.Join(s.Directory, "oauth-"+hex.EncodeToString(digest[:])+".json")
}

func (s FileStore) Get(account string) (OAuthCredentials, error) {
	data, err := os.ReadFile(s.path(account))
	if os.IsNotExist(err) {
		return OAuthCredentials{}, ErrCredentialsNotFound
	}
	if err != nil {
		return OAuthCredentials{}, fmt.Errorf("read fallback credential file: %w", err)
	}
	return decodeCredentials(string(data))
}

func (s FileStore) Set(account string, credentials OAuthCredentials) error {
	if err := os.MkdirAll(s.Directory, 0o700); err != nil {
		return fmt.Errorf("create fallback credential directory: %w", err)
	}
	value, err := encodeCredentials(credentials)
	if err != nil {
		return err
	}
	temporary, err := os.CreateTemp(s.Directory, ".oauth-*.tmp")
	if err != nil {
		return fmt.Errorf("create fallback credential file: %w", err)
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0o600); err != nil {
		temporary.Close()
		return err
	}
	if _, err := temporary.WriteString(value); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	path := s.path(account)
	if err := atomicfile.Replace(temporaryPath, path); err != nil {
		return fmt.Errorf("replace fallback credential file: %w", err)
	}
	return os.Chmod(path, 0o600)
}

func (s FileStore) Delete(account string) error {
	err := os.Remove(s.path(account))
	if os.IsNotExist(err) {
		return nil
	}
	return err
}

// FallbackStore uses the OS keyring first. File fallback is never automatic:
// the caller must explicitly set AllowFile after warning the user.
type FallbackStore struct {
	Primary   CredentialStore
	File      CredentialStore
	AllowFile bool
	Warn      func(string)
}

func (s FallbackStore) Get(account string) (OAuthCredentials, error) {
	credentials, err := s.Primary.Get(account)
	if err == nil || errors.Is(err, ErrCredentialsNotFound) || !s.AllowFile {
		if errors.Is(err, ErrCredentialsNotFound) && s.AllowFile {
			return s.File.Get(account)
		}
		return credentials, err
	}
	if s.Warn != nil {
		s.Warn("Warning: the operating-system credential store is unavailable; using an owner-only fallback file.")
	}
	return s.File.Get(account)
}

func (s FallbackStore) Set(account string, credentials OAuthCredentials) error {
	if err := s.Primary.Set(account, credentials); err == nil {
		_ = s.File.Delete(account)
		return nil
	} else if !s.AllowFile {
		return fmt.Errorf("%w; rerun with --allow-insecure-storage to opt in to an owner-only fallback file", err)
	}
	if s.Warn != nil {
		s.Warn("Warning: the operating-system credential store is unavailable; storing OAuth tokens in an owner-only fallback file.")
	}
	return s.File.Set(account, credentials)
}

func (s FallbackStore) Delete(account string) error {
	primaryErr := s.Primary.Delete(account)
	fileErr := s.File.Delete(account)
	if primaryErr != nil && !s.AllowFile {
		return primaryErr
	}
	return fileErr
}
