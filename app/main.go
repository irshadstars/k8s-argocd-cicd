// A deliberately small HTTP service whose only job is to make a deployment
// visible. The root handler prints the version and git commit baked in at build
// time, so you can tell by eye whether the pipeline actually shipped your code —
// rather than trusting that "Synced/Healthy" means what you hoped.
//
// Standard library only. No dependencies means no go.sum, no vulnerability
// surface, and a build that cannot break because of someone else's release.
package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
)

// Injected at build time via -ldflags "-X main.version=...". The defaults are
// what you see when running a local `go build` with no flags, which is a useful
// signal in itself: "dev" means this binary did not come from CI.
var (
	version = "dev"
	commit  = "none"
	built   = "unknown"
)

func main() {
	// Port 8080, not 80. The container runs as UID 65532 (non-root), and binding
	// a port below 1024 traditionally requires CAP_NET_BIND_SERVICE — which the
	// pod spec drops along with every other capability. Some runtimes now permit
	// it anyway, but relying on that makes the image dependent on host sysctls.
	addr := ":8080"

	hostname, err := os.Hostname()
	if err != nil {
		// Not fatal: the pod name is a convenience for showing which replica
		// answered, not something the service needs to function.
		hostname = "unknown"
	}

	mux := http.NewServeMux()

	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		// Explicitly 404 anything that is not the root. Without this, ServeMux's
		// "/" pattern silently matches every unrouted path, so a typo in a probe
		// path would return 200 and the probe would pass while testing nothing.
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		fmt.Fprintf(w, "Hello from k8s-argocd-cicd\n\n")
		fmt.Fprintf(w, "version:  %s\n", version)
		fmt.Fprintf(w, "commit:   %s\n", commit)
		fmt.Fprintf(w, "built:    %s\n", built)
		fmt.Fprintf(w, "pod:      %s\n", hostname)
	})

	// Liveness: "is the process wedged?" Answering at all is the whole test, so
	// this must never depend on anything external — a liveness probe that checks
	// a database restarts healthy pods during a database outage.
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		fmt.Fprintln(w, "ok")
	})

	// Readiness: "should traffic come here?" Distinct from liveness because
	// during shutdown we want to stop receiving traffic without being killed.
	ready := true
	mux.HandleFunc("/readyz", func(w http.ResponseWriter, r *http.Request) {
		if !ready {
			w.WriteHeader(http.StatusServiceUnavailable)
			fmt.Fprintln(w, "shutting down")
			return
		}
		w.WriteHeader(http.StatusOK)
		fmt.Fprintln(w, "ok")
	})

	srv := &http.Server{
		Addr:    addr,
		Handler: mux,
		// Bound how long a slow or idle client can hold a connection. Without
		// these, a handful of stalled connections can exhaust the server.
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	// Graceful shutdown. Kubernetes sends SIGTERM and waits
	// terminationGracePeriodSeconds (30 by default) before SIGKILL. Exiting
	// immediately on SIGTERM drops in-flight requests, which shows up as a small
	// burst of 502s on every single rollout.
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGTERM, syscall.SIGINT)

	go func() {
		log.Printf("listening on %s (version=%s commit=%s)", addr, version, commit)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("server error: %v", err)
		}
	}()

	<-stop
	log.Println("SIGTERM received, draining")

	// Fail readiness first, then pause. Endpoint removal propagates to every
	// kube-proxy asynchronously, so a pod that stops accepting connections the
	// instant it starts draining still receives traffic for a moment. This pause
	// is what turns a zero-downtime rollout from a claim into a fact.
	ready = false
	time.Sleep(3 * time.Second)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		log.Printf("forced shutdown: %v", err)
	}
	log.Println("stopped")
}
