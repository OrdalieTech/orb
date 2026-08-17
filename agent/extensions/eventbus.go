package extensions

import (
	"context"
	"fmt"
	"sync"
)

type EventListener func(context.Context, any) error

type EventBus interface {
	Emit(context.Context, string, any) []error
	On(string, EventListener) func()
	Clear()
}

type eventBus struct {
	mu        sync.RWMutex
	nextID    uint64
	listeners map[string][]busListener
}

type busListener struct {
	id      uint64
	handler EventListener
}

func NewEventBus() EventBus {
	return &eventBus{listeners: make(map[string][]busListener)}
}

func (bus *eventBus) Emit(ctx context.Context, channel string, data any) []error {
	bus.mu.RLock()
	listeners := append([]busListener(nil), bus.listeners[channel]...)
	bus.mu.RUnlock()
	var errors []error
	for _, listener := range listeners {
		if listener.handler == nil {
			continue
		}
		if err := callEventListener(ctx, listener.handler, data); err != nil {
			errors = append(errors, err)
		}
	}
	return errors
}

func (bus *eventBus) On(channel string, handler EventListener) func() {
	bus.mu.Lock()
	bus.nextID++
	id := bus.nextID
	bus.listeners[channel] = append(bus.listeners[channel], busListener{id: id, handler: handler})
	bus.mu.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			bus.mu.Lock()
			listeners := bus.listeners[channel]
			for index, listener := range listeners {
				if listener.id == id {
					listeners = append(listeners[:index], listeners[index+1:]...)
					break
				}
			}
			if len(listeners) == 0 {
				delete(bus.listeners, channel)
			} else {
				bus.listeners[channel] = listeners
			}
			bus.mu.Unlock()
		})
	}
}

func (bus *eventBus) Clear() {
	bus.mu.Lock()
	clear(bus.listeners)
	bus.mu.Unlock()
}

func callEventListener(ctx context.Context, handler EventListener, data any) (err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("event handler panic: %v", recovered)
		}
	}()
	return handler(ctx, data)
}
