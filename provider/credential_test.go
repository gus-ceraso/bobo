package provider

import (
	"context"
	"sync/atomic"
)

// memoryCredentialStore is a test fixture, not a production store implementation.
// Its exclusive scope covers the update callback, including any token exchange.
type memoryCredentialStore struct {
	gate       chan struct{}
	credential Credential
	present    bool
	loads      atomic.Int32
	updates    atomic.Int32
	loadErr    error
	updateErr  error
	commitErr  error
}

func newMemoryStore(credential Credential) *memoryCredentialStore {
	return &memoryCredentialStore{
		gate:       make(chan struct{}, 1),
		credential: credential,
		present:    credential != (Credential{}),
	}
}

func (s *memoryCredentialStore) lock(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	select {
	case s.gate <- struct{}{}:
		if err := ctx.Err(); err != nil {
			<-s.gate
			return err
		}
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *memoryCredentialStore) Load(ctx context.Context) (Credential, error) {
	s.loads.Add(1)
	if err := s.lock(ctx); err != nil {
		return Credential{}, err
	}
	defer func() { <-s.gate }()
	if s.loadErr != nil {
		return Credential{}, s.loadErr
	}
	if !s.present {
		return Credential{}, ErrNoCredentials
	}
	return s.credential, nil
}

func (s *memoryCredentialStore) Update(ctx context.Context, update func(Credential) (Credential, error)) (Credential, error) {
	s.updates.Add(1)
	if err := s.lock(ctx); err != nil {
		return Credential{}, err
	}
	defer func() { <-s.gate }()
	if s.updateErr != nil {
		return Credential{}, s.updateErr
	}
	credential, err := update(s.credential)
	if err != nil {
		return Credential{}, err
	}
	if s.commitErr != nil {
		return Credential{}, s.commitErr
	}
	if err := ctx.Err(); err != nil {
		return Credential{}, err
	}
	s.credential, s.present = credential, true
	return credential, nil
}

type credentialStoreFuncs struct {
	load   func(context.Context) (Credential, error)
	update func(context.Context, func(Credential) (Credential, error)) (Credential, error)
}

func (s credentialStoreFuncs) Load(ctx context.Context) (Credential, error) {
	return s.load(ctx)
}

func (s credentialStoreFuncs) Update(ctx context.Context, update func(Credential) (Credential, error)) (Credential, error) {
	return s.update(ctx, update)
}
