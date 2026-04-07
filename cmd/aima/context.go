package main

import (
	"github.com/jguan/aima/internal/agent"
	"github.com/jguan/aima/internal/k3s"
	"github.com/jguan/aima/internal/knowledge"
	"github.com/jguan/aima/internal/proxy"
	"github.com/jguan/aima/internal/runtime"
	"github.com/jguan/aima/internal/support"

	state "github.com/jguan/aima/internal"
)

// appContext holds shared state used across tool dependency builders.
// It collects the local variables that buildToolDeps() closures previously
// captured, enabling those closures to be split into separate files.
type appContext struct {
	cat      *knowledge.Catalog
	db       *state.DB
	kStore   *knowledge.Store
	rt       runtime.Runtime // default runtime (K3S > Docker > Native)
	nativeRt runtime.Runtime
	dockerRt runtime.Runtime
	k3sRt    runtime.Runtime
	proxy    *proxy.Server
	k3s      *k3s.Client
	dataDir  string
	digests  map[string]string // factory catalog digests
	support  *support.Service
	eventBus *agent.EventBus // shared EventBus for Explorer events
}
