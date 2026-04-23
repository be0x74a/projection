/*
Copyright 2024.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

// Package e2e_upgrade hosts the v0.1 -> v0.2 upgrade-path end-to-end test.
// It is separate from test/e2e/ because the suite owns the operator install
// lifecycle — helm install the old chart, migrate, helm upgrade to the current
// chart — rather than assuming a pre-installed operator.
package e2e_upgrade

import (
	"fmt"
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestE2EUpgrade(t *testing.T) {
	RegisterFailHandler(Fail)
	_, _ = fmt.Fprintf(GinkgoWriter, "Starting projection upgrade suite\n")
	RunSpecs(t, "e2e upgrade suite")
}
