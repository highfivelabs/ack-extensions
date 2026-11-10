// Copyright 2026 High Five Labs
// SPDX-License-Identifier: Apache-2.0

package e2e

import (
	"context"
	"net/http"
	"os/exec"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
)

func bad() {
	cfg := aws.Config{Region: "us-west-2"}
	cfg2, _ := awsconfig.LoadDefaultConfig(context.Background())
	cfg2.HTTPClient = nil
	_ = exec.Command("moto_server", "-p", "5000").Start()
	_, _ = http.Get("http://127.0.0.1:5000/moto-api/")
	_ = "https://sts.us-east-1.amazonaws.com"
	_ = "iam.us-gov-west-1.amazonaws.com"
	_ = cfg
}
