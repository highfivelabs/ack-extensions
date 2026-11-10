// Copyright 2026 High Five Labs
// SPDX-License-Identifier: Apache-2.0

package fake

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/iam"
	iamtypes "github.com/aws/aws-sdk-go-v2/service/iam/types"
)

// ---- AWS::IAM::VirtualMFADevice on moto's IAM ----

// VirtualMFADevice is the IAM virtual MFA device backend, the handler's
// calls in the schema's order: CreateVirtualMFADevice, then EnableMFADevice
// for each user; the read is ListVirtualMFADevices; the delete
// DeactivateMFADevice, then DeleteVirtualMFADevice. moto 5.2.5 has no tags
// for MFA devices (CreateVirtualMFADevice ignores them, and there is no
// TagMFADevice or ListMFADeviceTags), so the backend keeps Tags itself.
// moto takes any two codes in EnableMFADevice; AWS checks them against the
// seed, which the real handler holds from the create and nobody else ever
// sees. The identifier is the serial number (the device's ARN).
type VirtualMFADevice struct {
	IAM  *iam.Client
	mu   sync.Mutex
	tags map[string]any // serial -> Tags
	n    int
}

// NewVirtualMFADevice makes the backend.
func NewVirtualMFADevice(cfg aws.Config) *VirtualMFADevice {
	return &VirtualMFADevice{IAM: iam.NewFromConfig(cfg), tags: map[string]any{}}
}

func (b *VirtualMFADevice) Create(ctx context.Context, m map[string]any) (string, error) {
	name := str(m, "VirtualMfaDeviceName")
	if name == "" {
		b.mu.Lock()
		b.n++
		name = fmt.Sprintf("virtualmfadevice-%d", b.n)
		b.mu.Unlock()
	}
	in := &iam.CreateVirtualMFADeviceInput{VirtualMFADeviceName: aws.String(name)}
	if p := str(m, "Path"); p != "" {
		in.Path = aws.String(p)
	}
	out, err := b.IAM.CreateVirtualMFADevice(ctx, in)
	if err != nil {
		if code(err) == "EntityAlreadyExists" {
			return "", &HandlerError{"AlreadyExists", err.Error()}
		}
		return "", InvalidRequest("%v", err)
	}
	serial := aws.ToString(out.VirtualMFADevice.SerialNumber)
	for _, u := range strList(m["Users"]) {
		if _, err := b.IAM.EnableMFADevice(ctx, &iam.EnableMFADeviceInput{
			UserName: aws.String(u), SerialNumber: aws.String(serial),
			AuthenticationCode1: aws.String("123456"), AuthenticationCode2: aws.String("654321"),
		}); err != nil {
			// A handler that fails after the create leaves nothing behind.
			_, _ = b.IAM.DeleteVirtualMFADevice(ctx, &iam.DeleteVirtualMFADeviceInput{SerialNumber: aws.String(serial)})
			if code(err) == "NoSuchEntity" {
				return "", NotFound("%v", err)
			}
			return "", InvalidRequest("%v", err)
		}
	}
	if v, ok := m["Tags"]; ok {
		b.mu.Lock()
		b.tags[serial] = v
		b.mu.Unlock()
	}
	return serial, nil
}

func (b *VirtualMFADevice) find(ctx context.Context, serial string) (*iamtypes.VirtualMFADevice, error) {
	p := iam.NewListVirtualMFADevicesPaginator(b.IAM, &iam.ListVirtualMFADevicesInput{})
	for p.HasMorePages() {
		out, err := p.NextPage(ctx)
		if err != nil {
			return nil, err
		}
		for i := range out.VirtualMFADevices {
			if aws.ToString(out.VirtualMFADevices[i].SerialNumber) == serial {
				return &out.VirtualMFADevices[i], nil
			}
		}
	}
	return nil, NotFound("%s", serial)
}

func (b *VirtualMFADevice) Read(ctx context.Context, serial string) (map[string]any, error) {
	d, err := b.find(ctx, serial)
	if err != nil {
		return nil, err
	}
	// arn:aws:iam::<account>:mfa<path><name>
	full := serial[strings.Index(serial, ":mfa")+len(":mfa"):]
	i := strings.LastIndex(full, "/")
	m := map[string]any{
		"SerialNumber":         serial,
		"Path":                 full[:i+1],
		"VirtualMfaDeviceName": full[i+1:],
		"Users":                []any{},
	}
	if d.User != nil {
		m["Users"] = []any{aws.ToString(d.User.UserName)}
	}
	b.mu.Lock()
	if v, ok := b.tags[serial]; ok {
		m["Tags"] = v
	}
	b.mu.Unlock()
	return m, nil
}

// Update changes tags only: the handler's update permissions are
// iam:TagMFADevice and iam:UntagMFADevice. What the real handler does with
// a changed user list is not known without AWS; the fake refuses it.
func (b *VirtualMFADevice) Update(ctx context.Context, serial string, prev, next map[string]any) error {
	if strings.Join(strList(prev["Users"]), ",") != strings.Join(strList(next["Users"]), ",") {
		return &HandlerError{"NotUpdatable", "the fake does not change a virtual MFA device's users: the handler's update has only iam:TagMFADevice and iam:UntagMFADevice"}
	}
	if _, err := b.find(ctx, serial); err != nil {
		return err
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if v, ok := next["Tags"]; ok {
		b.tags[serial] = v
	} else {
		delete(b.tags, serial)
	}
	return nil
}

func (b *VirtualMFADevice) Delete(ctx context.Context, serial string) error {
	d, err := b.find(ctx, serial)
	if err != nil {
		return err
	}
	if d.User != nil {
		if _, err := b.IAM.DeactivateMFADevice(ctx, &iam.DeactivateMFADeviceInput{
			UserName: d.User.UserName, SerialNumber: aws.String(serial),
		}); err != nil {
			return err
		}
	}
	if _, err := b.IAM.DeleteVirtualMFADevice(ctx, &iam.DeleteVirtualMFADeviceInput{SerialNumber: aws.String(serial)}); err != nil {
		return err
	}
	b.mu.Lock()
	delete(b.tags, serial)
	b.mu.Unlock()
	return nil
}
