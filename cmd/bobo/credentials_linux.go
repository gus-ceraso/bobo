//go:build linux

package main

import (
	"context"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"ceraso.dev/bobo/internal/provider"
	"golang.org/x/sys/unix"
)

// credentialRecord is bobo's on-disk format; provider.Credential stays independent
// of application storage and configuration conventions.
type credentialRecord struct {
	ClientID      string    `json:"client_id,omitempty"`
	AccessToken   string    `json:"access_token,omitempty"`
	RefreshToken  string    `json:"refresh_token,omitempty"`
	ExpiresAt     time.Time `json:"expires_at,omitempty"`
	FallbackToken string    `json:"fallback_token,omitempty"`
}

func record(c provider.Credential) credentialRecord {
	return credentialRecord{c.ClientID, c.AccessToken, c.RefreshToken, c.ExpiresAt, c.FallbackToken}
}

func (r credentialRecord) credential() provider.Credential {
	return provider.Credential{ClientID: r.ClientID, AccessToken: r.AccessToken, RefreshToken: r.RefreshToken, ExpiresAt: r.ExpiresAt, FallbackToken: r.FallbackToken}
}

type credentialStore struct {
	path     string
	fallback string // Only consulted when the file is absent; never saved by Load.
}

func (s *credentialStore) Load(ctx context.Context) (provider.Credential, error) {
	if err := ctx.Err(); err != nil {
		return provider.Credential{}, err
	}
	file, err := os.OpenFile(s.path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if errors.Is(err, os.ErrNotExist) {
		if s.fallback != "" {
			return provider.Credential{FallbackToken: s.fallback}, nil
		}
		return provider.Credential{}, provider.ErrNoCredentials
	}
	if err != nil {
		return provider.Credential{}, fmt.Errorf("open credentials: %w", err)
	}
	defer file.Close()
	if err := privateRegularFile(file); err != nil {
		return provider.Credential{}, err
	}
	body, err := io.ReadAll(io.LimitReader(file, (1<<20)+1))
	if err != nil {
		return provider.Credential{}, fmt.Errorf("read credentials: %w", err)
	}
	var r credentialRecord
	// Decoder diagnostics may quote secret input. Never expose them.
	if len(body) > 1<<20 || json.Unmarshal(body, &r) != nil || strings.TrimSpace(string(body)) == "null" {
		return provider.Credential{}, errors.New("invalid credentials.json")
	}
	if err := ctx.Err(); err != nil {
		return provider.Credential{}, err
	}
	return r.credential(), nil
}

func privateRegularFile(file *os.File) error {
	info, err := file.Stat()
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return errors.New("credential and lock files must be regular files")
	}
	if info.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf("%s must not be accessible by group or others (use chmod 600)", file.Name())
	}
	return nil
}

func (s *credentialStore) Update(ctx context.Context, update func(provider.Credential) (provider.Credential, error)) (provider.Credential, error) {
	if err := ctx.Err(); err != nil {
		return provider.Credential{}, err
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return provider.Credential{}, err
	}
	// This inode must remain stable across credential-file renames. Never unlink
	// the lock file: another process may already be waiting on that inode.
	lock, err := os.OpenFile(s.path+".lock", os.O_RDWR|os.O_CREATE|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0o600)
	if err != nil {
		return provider.Credential{}, err
	}
	defer lock.Close()
	if err := privateRegularFile(lock); err != nil {
		return provider.Credential{}, err
	}
	for {
		err = unix.Flock(int(lock.Fd()), unix.LOCK_EX|unix.LOCK_NB)
		if err == nil {
			break
		}
		if err != unix.EWOULDBLOCK && err != unix.EAGAIN && err != unix.EINTR {
			return provider.Credential{}, err
		}
		select {
		case <-ctx.Done():
			return provider.Credential{}, ctx.Err()
		case <-time.After(20 * time.Millisecond):
		}
	}
	defer unix.Flock(int(lock.Fd()), unix.LOCK_UN)
	if err := ctx.Err(); err != nil {
		return provider.Credential{}, err
	}
	current, err := s.Load(ctx)
	if err != nil && !errors.Is(err, provider.ErrNoCredentials) {
		return provider.Credential{}, err
	}
	next, err := update(current)
	if err != nil {
		return provider.Credential{}, err
	}
	if err := ctx.Err(); err != nil {
		return provider.Credential{}, err
	}
	if next == current {
		return next, nil
	}
	body, err := json.Marshal(record(next))
	if err != nil {
		return provider.Credential{}, errors.New("encode credentials")
	}
	tmp, err := os.CreateTemp(filepath.Dir(s.path), ".credentials-*")
	if err != nil {
		return provider.Credential{}, err
	}
	defer os.Remove(tmp.Name())
	defer tmp.Close()
	if _, err := tmp.Write(body); err != nil {
		return provider.Credential{}, err
	}
	if err := tmp.Sync(); err != nil {
		return provider.Credential{}, err
	}
	if err := tmp.Close(); err != nil {
		return provider.Credential{}, err
	}
	if err := ctx.Err(); err != nil {
		return provider.Credential{}, err
	}
	if err := os.Rename(tmp.Name(), s.path); err != nil {
		return provider.Credential{}, err
	}
	dir, err := os.Open(filepath.Dir(s.path))
	if err != nil {
		return provider.Credential{}, err
	}
	defer dir.Close()
	if err := dir.Sync(); err != nil {
		return provider.Credential{}, err
	}
	return next, nil
}
