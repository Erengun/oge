//go:build unix

package run

import (
	"errors"
	"syscall"
	"testing"
	"time"

	"github.com/erengun/oge/internal/agent/fake/multiturn"
)

// Closing the Chat mid-Consultation cancels it: Close waits for its
// ConsultationEnded, and no advisor process outlives it.
func TestCloseEndsAConsultation(t *testing.T) {
	c := openChat(t, multiturn.Scenario{}, 0)
	withAdvisor(c, advisorScript(t, "echo thinking\nsleep 30\n"))
	done := make(chan error, 1)
	go func() {
		_, err := c.Consult(t.Context(), "anything?")
		done <- err
	}()
	var proc map[string]any
	for limit := time.Now().Add(10 * time.Second); proc == nil; {
		for _, r := range recordsOf(t, c, RecProcessStarted) {
			if r["consultation"] != nil {
				proc = r
			}
		}
		if time.Now().After(limit) {
			t.Fatal("the advisor never started")
		}
		time.Sleep(20 * time.Millisecond)
	}
	c.Close()
	select {
	case err := <-done:
		if err == nil {
			t.Error("a Consultation the Chat closed under returned advice")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Consult outlived Close")
	}
	ended := recordsOf(t, c, RecConsultationEnded)
	if len(ended) != 1 || ended[0]["outcome"] != ConsultCancelled {
		t.Errorf("ConsultationEnded %v", ended)
	}
	if pgid := int(proc["pgid"].(float64)); pgid <= 0 || !errors.Is(syscall.Kill(-pgid, 0), syscall.ESRCH) {
		t.Errorf("the advisor's process group %d outlived Close", pgid)
	}
	if _, err := c.Consult(t.Context(), "again?"); !errors.Is(err, ErrChatClosed) {
		t.Errorf("Consult after Close: %v", err)
	}
}
