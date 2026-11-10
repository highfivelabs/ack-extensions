// Copyright 2026 High Five Labs
// SPDX-License-Identifier: Apache-2.0

package test

import (
	"os"
	"os/exec"

	"github.com/highfivelabs/ack-extensions/pkg/fakeaws"
)

const xml = `<R xmlns="http://rds.amazonaws.com/doc/2014-10-31/"></R>`

// Principals and annotation keys name AWS but are not endpoints.
var notEndpoints = []string{"pods.eks.amazonaws.com", "eks.amazonaws.com/role-arn", "arn:aws:iam::123456789012:role/x"}

func good() error {
	if err := exec.Command("go", "build", "./...").Run(); err != nil {
		return err
	}
	cmd := exec.Command("moto_server", "-p", "5000")
	cmd.Env = fakeaws.Isolated("PATH=" + os.Getenv("PATH"))
	cfg, err := fakeaws.Config("http://127.0.0.1:5000", "us-west-2")
	cfg.RetryMaxAttempts = 1
	_ = cfg
	return err
}
