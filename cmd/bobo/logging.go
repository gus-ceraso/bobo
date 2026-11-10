//go:build linux

package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"sync"

	"ceraso.dev/bobo/internal/provider"
)

type logSink struct {
	mu     sync.Mutex
	file   *os.File
	cancel context.CancelFunc
	err    error
}

func (s *logSink) Write(body []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	n, err := s.file.Write(body)
	if err == nil && n != len(body) {
		err = io.ErrShortWrite
	}
	if err != nil && s.err == nil {
		s.err = fmt.Errorf("write log: %w", err)
		s.cancel()
	}
	return n, err
}

func (s *logSink) failure() error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.err
}

func loggedError(client *provider.Client, sink *logSink, err error) error {
	if err != nil && client.Logger != nil {
		client.Logger.Printf("event=application.failed error=%q", err)
	}
	if logErr := sink.failure(); logErr != nil {
		return logErr
	}
	return err
}
