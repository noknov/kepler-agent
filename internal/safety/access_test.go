package safety

import "testing"

func TestAccessPolicy(t *testing.T) {
	p := NewAccessPolicy([]string{"U1"}, []string{"C1"})
	if !p.IsAllowed("U1", "C1") {
		t.Fatal("expected allowed user/channel")
	}
	if p.IsAllowed("U2", "C1") {
		t.Fatal("expected denied user")
	}
	if p.IsAllowed("U1", "C2") {
		t.Fatal("expected denied channel")
	}
	if !p.AllowsUser("U1") {
		t.Fatal("expected allowlisted user")
	}
	if p.AllowsUser("U2") {
		t.Fatal("expected denied user from allowlist")
	}
	if !p.AllowsChannel("C1") {
		t.Fatal("expected allowlisted channel")
	}
	if p.AllowsChannel("C2") {
		t.Fatal("expected denied channel from allowlist")
	}
}
