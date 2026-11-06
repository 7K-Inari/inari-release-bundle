// Git write-path proof (B4): after the default git-config is in place and a
// platform-resources reconcile has been triggered, the tenant's state repo
// must materialize in the shared PVC and be served by the in-cluster git
// service (git daemon, E2E_GIT_URL). go-git speaks the git:// transport
// natively — no git binary needed in the seed's container.
package main

import (
	"fmt"
	"time"

	git "github.com/go-git/go-git/v5"
	gitcfg "github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/storage/memory"
)

// lsRemoteServed reports whether the git daemon serves the repo URL.
// A freshly initialized repo (no commits) still answers with an empty
// advertisement — served is about reachability, not content.
func lsRemoteServed(repoURL string) error {
	remote := git.NewRemote(memory.NewStorage(), &gitcfg.RemoteConfig{
		Name: "origin",
		URLs: []string{repoURL},
	})
	// List advertises refs without transferring objects.
	if _, err := remote.List(&git.ListOptions{}); err != nil {
		return fmt.Errorf("ls-remote %s: %w", repoURL, err)
	}
	return nil
}

// waitGitServed polls ls-remote until the repo is served or the deadline
// passes. The repo materializes asynchronously (reconcile → provider
// EnsureRepo/commit → PVC → daemon), so a single probe is never enough.
func waitGitServed(repoURL string, timeout, interval time.Duration) error {
	deadline := time.Now().Add(timeout)
	var last error
	for {
		if err := lsRemoteServed(repoURL); err == nil {
			return nil
		} else {
			last = err
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("git service never served the repo within %s: %w", timeout, last)
		}
		time.Sleep(interval)
	}
}
