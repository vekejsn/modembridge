package service

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"modembridge/internal/backend"
	"modembridge/internal/config"
	"modembridge/internal/modem"
	"modembridge/internal/protocol"
)

type Service struct {
	Config config.Config
	Log    *slog.Logger
	// Open is injectable so the complete call lifecycle can be tested without hardware.
	Open      func(config.Modem) (*modem.Modem, error)
	endpoints map[string]*backend.Endpoint
}

func (s *Service) Run(ctx context.Context) error {
	if err := s.Config.Validate(); err != nil {
		return err
	}
	if s.Log == nil {
		s.Log = slog.Default()
	}
	if s.Open == nil {
		s.Open = modem.Open
	}
	s.endpoints = map[string]*backend.Endpoint{}
	defer func() {
		for _, e := range s.endpoints {
			e.Close()
		}
	}()
	for _, r := range s.Config.Routes {
		e, err := backend.New(r)
		if err != nil {
			return fmt.Errorf("backend %s: %w", r.Protocol, err)
		}
		s.endpoints[r.Protocol] = e
	}
	for ctx.Err() == nil {
		err := s.call(ctx)
		if ctx.Err() != nil {
			break
		}
		if err != nil {
			s.Log.Warn("modem cycle ended", "error", err)
		}
		timer := time.NewTimer(time.Duration(s.Config.RecoverySeconds) * time.Second)
		select {
		case <-ctx.Done():
			timer.Stop()
		case <-timer.C:
		}
	}
	return nil
}

func (s *Service) call(ctx context.Context) error {
	m, err := s.Open(s.Config.Modem)
	if err != nil {
		return err
	}
	defer func() {
		if err := m.Close(); err != nil {
			s.Log.Error("modem close failed", "error", err)
		}
	}()
	stop := context.AfterFunc(ctx, func() { _ = m.Close() })
	defer stop()
	if err = m.Initialize(ctx, s.Config.Modem); err != nil {
		return err
	}
	s.Log.Info("modem ready", "device", s.Config.Modem.Device)
	connection, err := m.Answer(ctx, s.Config.Modem)
	if err != nil {
		return err
	}
	var idBytes [12]byte
	if _, err = rand.Read(idBytes[:]); err != nil {
		return err
	}
	id := hex.EncodeToString(idBytes[:])
	connectedAt := time.Now().UTC().Format(time.RFC3339Nano)
	log := s.Log.With("session", id)
	log.Info("call connected", "result", connection)
	sessionCtx, cancel := context.WithTimeout(ctx, time.Duration(s.Config.MaxSessionSeconds)*time.Second)
	defer cancel()
	stopSession := context.AfterFunc(sessionCtx, func() { _ = m.Close() })
	defer stopSession()
	detectCtx, stopDetect := context.WithTimeout(sessionCtx, time.Duration(s.Config.IdentificationSeconds)*time.Second)
	names := make([]string, 0, len(s.Config.Routes))
	for _, r := range s.Config.Routes {
		names = append(names, r.Protocol)
	}
	name, opening, err := Identify(detectCtx, m, names, s.Config.MaxIdentificationBytes)
	stopDetect()
	if err != nil {
		log.Warn("identification failed", "error", err)
		return err
	}
	log.Info("call identified", "protocol", name, "buffered_bytes", len(opening))
	attachCtx, stopAttach := context.WithTimeout(sessionCtx, time.Duration(s.Config.BackendSeconds)*time.Second)
	b, err := s.endpoints[name].Attach(attachCtx, map[string]any{"version": 1, "session_id": id, "protocol": name, "connect_result": connection, "connected_at": connectedAt})
	stopAttach()
	if err != nil {
		return fmt.Errorf("attach %s: %w", name, err)
	}
	defer b.Close()
	log.Info("backend attached", "protocol", name)
	err = Bridge(sessionCtx, m, b, opening, time.Duration(s.Config.IdleSeconds)*time.Second)
	log.Info("call ended", "reason", fmt.Sprint(err))
	return err
}

type Source interface {
	Read(context.Context) ([]byte, error)
}

func Identify(ctx context.Context, source Source, names []string, limit int) (string, []byte, error) {
	buffer := make([]byte, 0, min(limit, 4096))
	for {
		data, err := source.Read(ctx)
		if err != nil {
			return "", buffer, fmt.Errorf("unknown protocol: %w", err)
		}
		if len(data) > limit-len(buffer) {
			return "", buffer, errors.New("identification buffer limit exceeded")
		}
		buffer = append(buffer, data...)
		name, err := protocol.Detect(buffer, names)
		if err != nil {
			return "", buffer, err
		}
		if name != "" {
			return name, buffer, nil
		}
		if len(buffer) == limit {
			return "", buffer, errors.New("unknown protocol at buffer limit")
		}
	}
}

type packet struct {
	data      []byte
	err       error
	fromPhone bool
}

// Bridge has a single writer per destination, bounded input queues, and no
// protocol-level side effects. Closing both transports releases its workers.
func Bridge(parent context.Context, m *modem.Modem, b *backend.Session, opening []byte, idle time.Duration) error {
	ctx, cancel := context.WithCancelCause(parent)
	timer := time.AfterFunc(idle, func() { cancel(errors.New("session idle timeout")) })
	defer timer.Stop()
	defer cancel(nil)
	stop := context.AfterFunc(ctx, func() { b.Close(); _ = m.Close() })
	defer stop()
	var workers sync.WaitGroup
	packets := make(chan packet, 2)
	spawn := func(phone bool, read func() ([]byte, error)) {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for {
				data, err := read()
				select {
				case packets <- packet{data, err, phone}:
				case <-ctx.Done():
					return
				}
				if err != nil {
					return
				}
			}
		}()
	}
	finish := func(reason error) error {
		b.End(fmt.Sprint(reason))
		cancel(reason)
		b.Close()
		_ = m.Close()
		workers.Wait()
		return reason
	}
	if err := b.Send(opening); err != nil {
		return finish(err)
	}
	spawn(true, func() ([]byte, error) { return m.Read(ctx) })
	spawn(false, b.Receive)
	for {
		select {
		case <-ctx.Done():
			return finish(context.Cause(ctx))
		case p := <-packets:
			if len(p.data) > 0 {
				timer.Reset(idle)
				var err error
				if p.fromPhone {
					err = b.Send(p.data)
				} else {
					err = m.Write(p.data)
				}
				if err != nil {
					return finish(err)
				}
			}
			if p.err != nil {
				return finish(p.err)
			}
		}
	}
}
