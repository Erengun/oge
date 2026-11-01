//go:build linux

package proc

import (
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"syscall"
	"testing"
	"time"
)

// The thread that forked a command is held until Wait reaps it: the death
// signal follows that thread, so the thread going back to Go's pool (where
// any goroutine exiting locked could end it) would kill the command early.
// Once reaped, the thread ends, unless it is the main thread, which Go
// parks instead of ending.
func TestTheForkingThreadLivesUntilReaped(t *testing.T) {
	// The first fork in a test binary tends to land on the main thread. A
	// throwaway one parks it there, so the fork under test gets a thread
	// that can end.
	first := exec.Command("true")
	if err := Start(first); err != nil {
		t.Fatal(err)
	}
	_ = Wait(first)
	tids := make(chan int, 1)
	forkedOn = func(tid int) { tids <- tid }
	t.Cleanup(func() { forkedOn = nil })
	cmd := exec.Command("sleep", "30")
	if err := Start(cmd); err != nil {
		t.Fatal(err)
	}
	forkedOn = nil
	tid := <-tids
	task := "/proc/self/task/" + strconv.Itoa(tid)
	alive := func() bool { _, err := os.Stat(task); return err == nil }
	// Other goroutines come and go, some exiting locked to their threads.
	for range 64 {
		done := make(chan struct{})
		go func() {
			defer close(done)
			runtime.LockOSThread()
		}()
		<-done
	}
	time.Sleep(50 * time.Millisecond)
	if !alive() {
		t.Fatal("the forking thread ended while its command lives")
	}
	if err := syscall.Kill(cmd.Process.Pid, 0); err != nil {
		t.Fatalf("the command died before Wait: %v", err)
	}
	Kill(cmd)
	_ = Wait(cmd)
	if tid == os.Getpid() {
		t.Log("forked on the main thread, which Go parks rather than ends")
		return
	}
	deadline := time.Now().Add(2 * time.Second)
	for alive() {
		if time.Now().After(deadline) {
			t.Fatal("the forking thread outlived Wait")
		}
		time.Sleep(5 * time.Millisecond)
	}
}
