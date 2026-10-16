package kernel

import (
	"context"
	"errors"
	"sync"
)

// ServiceManager orchestrates the lifecycle of multiple services.
type ServiceManager struct {
	mu       sync.RWMutex
	services []Service
	started  bool
}

// NewServiceManager creates a new service manager.
func NewServiceManager() *ServiceManager {
	return &ServiceManager{
		services: make([]Service, 0),
	}
}

// Register adds a service to the manager.
func (m *ServiceManager) Register(service Service) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.services = append(m.services, service)
}

// StartAll starts all registered services in order.
func (m *ServiceManager) StartAll(ctx context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.started {
		return errors.New("services already started")
	}

	for i, svc := range m.services {
		if err := svc.Start(ctx); err != nil {
			// Rollback: stop all successfully started services
			for j := i - 1; j >= 0; j-- {
				_ = m.services[j].Stop(ctx)
			}
			return err
		}
	}

	m.started = true
	return nil
}

// StopAll stops all registered services in reverse order.
func (m *ServiceManager) StopAll(ctx context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if !m.started {
		return nil
	}

	var errs []error
	for i := len(m.services) - 1; i >= 0; i-- {
		if err := m.services[i].Stop(ctx); err != nil {
			errs = append(errs, err)
		}
	}

	m.started = false

	if len(errs) > 0 {
		return errors.Join(errs...)
	}
	return nil
}
