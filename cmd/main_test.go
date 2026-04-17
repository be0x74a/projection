/*
Copyright 2024.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package main

import (
	"flag"
	"testing"
	"time"
)

func TestBindFlagsDefaults(t *testing.T) {
	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	cfg := bindFlags(fs)
	if err := fs.Parse([]string{}); err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if cfg.requeueInterval != 30*time.Second {
		t.Errorf("requeueInterval = %v, want 30s", cfg.requeueInterval)
	}
	if cfg.leaderElectionLeaseDuration != 15*time.Second {
		t.Errorf("leaderElectionLeaseDuration = %v, want 15s", cfg.leaderElectionLeaseDuration)
	}
	if cfg.sourceModeFlag != "allowlist" {
		t.Errorf("sourceModeFlag = %q, want allowlist", cfg.sourceModeFlag)
	}
	if cfg.enableLeaderElection {
		t.Error("enableLeaderElection default should be false")
	}
	if cfg.probeAddr != ":8081" {
		t.Errorf("probeAddr = %q, want :8081", cfg.probeAddr)
	}
}

func TestBindFlagsOverride(t *testing.T) {
	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	cfg := bindFlags(fs)
	args := []string{
		"--requeue-interval=5s",
		"--leader-election-lease-duration=45s",
		"--source-mode=permissive",
		"--leader-elect",
	}
	if err := fs.Parse(args); err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if cfg.requeueInterval != 5*time.Second {
		t.Errorf("requeueInterval = %v, want 5s", cfg.requeueInterval)
	}
	if cfg.leaderElectionLeaseDuration != 45*time.Second {
		t.Errorf("leaderElectionLeaseDuration = %v, want 45s", cfg.leaderElectionLeaseDuration)
	}
	if cfg.sourceModeFlag != "permissive" {
		t.Errorf("sourceModeFlag = %q, want permissive", cfg.sourceModeFlag)
	}
	if !cfg.enableLeaderElection {
		t.Error("enableLeaderElection should be true after --leader-elect")
	}
}
