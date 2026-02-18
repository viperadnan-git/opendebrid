package node

import "sync"

// NodeClientStore is a thread-safe store for NodeClient instances.
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
	s.clients[id] = client
}

func (s *NodeClientStore) Delete(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.clients, id)
}
