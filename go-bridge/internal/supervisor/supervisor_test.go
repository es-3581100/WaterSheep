package supervisor

import (
	"net/http/httptest"
	"testing"
)

func TestStatusOwnershipIsReadOnlyGoModule(t *testing.T) {
	s, err := New(Config{ChildCommand: []string{"true"}})
	if err != nil {
		t.Fatal(err)
	}
	st := s.Snapshot()
	if st.Owner != SupervisorOwner || st.GoModule != SupervisorModule {
		t.Fatalf("unexpected ownership: %#v", st)
	}
	if st.Authority != ObservationAuthority {
		t.Fatalf("status must stay observation-only: %#v", st)
	}
}

func TestDebugRequiresDevAndExplicitApproval(t *testing.T) {
	s, err := New(Config{ChildCommand: []string{"true"}, DevMode: true})
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest("GET", "/v1/debug/logs", nil)
	if s.debugApproved(r) {
		t.Fatal("debug output must not be approved without header")
	}
	r.Header.Set(DebugApprovalHeader, DebugApprovalPhrase)
	if !s.debugApproved(r) {
		t.Fatal("explicit approval should unlock read-only debug output in dev mode")
	}

	prod, err := New(Config{ChildCommand: []string{"true"}, DevMode: false})
	if err != nil {
		t.Fatal(err)
	}
	if prod.debugApproved(r) {
		t.Fatal("approval must not unlock direct output outside dev mode")
	}
}

func TestRingBufferKeepsNewestBytes(t *testing.T) {
	r := newRingBuffer(5)
	_, _ = r.Write([]byte("abc"))
	_, _ = r.Write([]byte("def"))
	if got := r.String(); got != "bcdef" {
		t.Fatalf("got %q", got)
	}
}
