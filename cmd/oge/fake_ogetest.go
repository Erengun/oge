//go:build ogetest

package main

import (
	"os"

	"github.com/erengun/oge/internal/agent"
	"github.com/erengun/oge/internal/agent/fake"
)

// Test builds bind the scripted fake as agent "fake"; it runs the script
// named by OGE_FAKE_SCRIPT. OGE_TEST_SHARED_GOCACHE gives Checks a shared
// GOCACHE, which keeps the binary tests fast.
func init() {
	testAgents = map[string]agent.Adapter{fake.Name: fake.New(os.Getenv("OGE_FAKE_SCRIPT"))}
	testCheckGoCache = os.Getenv("OGE_TEST_SHARED_GOCACHE")
}
