package node

import (
	"io"
	"sync"
)

// NodeClientStore is a thread-safe store for NodeClient instances.
// Clients that implement io.Closer are closed on Delete or Close.
type NodeClientStore struct {
	mu      sync.RWMutex
	clients map[string]NodeClient
}

func NewNodeClientStore() *NodeClientStore {
	return &NodeClientStore{clients: make(map[string]NodeClient)}
}

func (s *NodeClientStore) Get(id string) (NodeClient, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	c, ok := s.clients[id]
	return c, ok
}

func (s *NodeClientStore) Set(id string, client NodeClient) {
	s.mu.Lock()
	defer s.mu.Unlock()
	// Close the previous client if being replaced.
	if prev, ok := s.clients[id]; ok {
		if closer, ok := prev.(io.Closer); ok {
			_ = closer.Close()
		}
	}
	s.clients[id] = client
}

func (s *NodeClientStore) Delete(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if c, ok := s.clients[id]; ok {
		if closer, ok := c.(io.Closer); ok {
			_ = closer.Close()
		}
		delete(s.clients, id)
	}
}

// Close closes all clients that implement io.Closer and empties the store.
func (s *NodeClientStore) Close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for id, c := range s.clients {
		if closer, ok := c.(io.Closer); ok {
			_ = closer.Close()
		}
		delete(s.clients, id)
	}
}
