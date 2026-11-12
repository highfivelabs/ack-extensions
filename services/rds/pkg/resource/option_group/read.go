// Copyright 2026 High Five Labs
// SPDX-License-Identifier: Apache-2.0

package option_group

import (
	svcsdk "github.com/aws/aws-sdk-go-v2/service/rds"
)

// describeByNameOnly drops the engine filters from a DescribeOptionGroups
// request that names its group. The generator fills every input field the
// spec has, but AWS's RDS API reference says OptionGroupName "can't be
// supplied together with EngineName or MajorEngineVersion"
// (https://docs.aws.amazon.com/AmazonRDS/latest/APIReference/API_DescribeOptionGroups.html):
// AWS refuses the request, which moto lets through. The name is the primary
// key, so it alone finds the group; its engine is read back from the reply.
// Called from the sdk_read_many_post_build_request hook (generator.yaml).
func describeByNameOnly(input *svcsdk.DescribeOptionGroupsInput) {
	if input.OptionGroupName == nil {
		return
	}
	input.EngineName = nil
	input.MajorEngineVersion = nil
}
