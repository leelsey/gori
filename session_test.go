package gori

import (
	"path/filepath"
	"sync"
	"testing"
)

func TestSessionConcurrentSaveSet(t *testing.T) {
	s := NewSession()
	s.Append(UserText("hi"))
	path := filepath.Join(t.TempDir(), "s.json")

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := 0; i < 300; i++ {
			s.Set("k", i)
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < 300; i++ {
			if err := s.Save(path); err != nil {
				t.Errorf("save: %v", err)
				return
			}
		}
	}()
	wg.Wait()
}
