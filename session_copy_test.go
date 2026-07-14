package gori

import "testing"

func TestHistoryContentIsCopied(t *testing.T) {
	s := NewSession()
	s.Append(UserText("secret"))

	hist := s.History()
	hist[0].Content[0] = Text{Text: "[redacted]"}

	if got := s.History()[0].Text(); got != "secret" {
		t.Fatalf("session history = %q after mutating a returned copy, want %q", got, "secret")
	}
}

func TestZeroValueRegistryRegister(t *testing.T) {
	var r Registry
	r.Register(echoTool())
	if _, ok := r.Get("echo"); !ok {
		t.Fatal("tool not registered on zero-value Registry")
	}
}

func TestZeroValueSessionSet(t *testing.T) {
	var s Session
	s.Set("k", 1)
	if v, ok := s.Get("k"); !ok || v != 1 {
		t.Fatalf("Get(k) = %v, %v; want 1, true", v, ok)
	}
}
