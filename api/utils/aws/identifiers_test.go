/*
Copyright 2022 Gravitational, Inc.

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

package aws

import (
	"strings"
	"testing"

	"github.com/gravitational/trace"
	"github.com/stretchr/testify/require"
)

func TestIsValidAccountID(t *testing.T) {
	isBadParamErrFn := func(tt require.TestingT, err error, i ...any) {
		require.True(tt, trace.IsBadParameter(err), "expected bad parameter, got %v", err)
	}

	for _, tt := range []struct {
		name      string
		accountID string
		errCheck  require.ErrorAssertionFunc
	}{
		{
			name:      "valid account id",
			accountID: "123456789012",
			errCheck:  require.NoError,
		},
		{
			name:      "empty",
			accountID: "",
			errCheck:  isBadParamErrFn,
		},
		{
			name:      "less digits",
			accountID: "12345678901",
			errCheck:  isBadParamErrFn,
		},
		{
			name:      "more digits",
			accountID: "1234567890123",
			errCheck:  isBadParamErrFn,
		},
		{
			name:      "invalid chars",
			accountID: "12345678901A",
			errCheck:  isBadParamErrFn,
		},
		{
			name:      "invalid chars with emojis",
			accountID: "12345678901✅",
			errCheck:  isBadParamErrFn,
		},
		{
			name:      "unicode digit is invalid",
			accountID: "123456789৩", // ৩ is a valid unicode digit and its len("৩") is 3
			errCheck:  isBadParamErrFn,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			tt.errCheck(t, IsValidAccountID(tt.accountID))
		})
	}
}

func TestIsValidIAMRoleName(t *testing.T) {
	isBadParamErrFn := func(tt require.TestingT, err error, i ...any) {
		require.True(tt, trace.IsBadParameter(err), "expected bad parameter, got %v", err)
	}

	for _, tt := range []struct {
		name     string
		role     string
		errCheck require.ErrorAssertionFunc
	}{
		{
			name:     "valid",
			role:     "valid",
			errCheck: require.NoError,
		},
		{
			name:     "valid with numbers",
			role:     "00VALID11",
			errCheck: require.NoError,
		},
		{
			name:     "only one symbol",
			role:     "_",
			errCheck: require.NoError,
		},
		{
			name:     "all symbols",
			role:     "Test+1=2,3.4@5-6_7",
			errCheck: require.NoError,
		},
		{
			name:     "empty",
			role:     "",
			errCheck: isBadParamErrFn,
		},
		{
			name:     "too large",
			role:     strings.Repeat("r", 65),
			errCheck: isBadParamErrFn,
		},
		{
			name:     "invalid symbols",
			role:     "role/admin",
			errCheck: isBadParamErrFn,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			tt.errCheck(t, IsValidIAMRoleName(tt.role))
		})
	}
}

func TestIsValidRegion(t *testing.T) {
	isBadParamErrFn := func(tt require.TestingT, err error, i ...any) {
		require.True(tt, trace.IsBadParameter(err), "expected bad parameter, got %v", err)
	}

	for _, tt := range []struct {
		name     string
		region   string
		errCheck require.ErrorAssertionFunc
	}{
		{
			name:     "us region",
			region:   "us-east-1",
			errCheck: require.NoError,
		},
		{
			name:     "eu region",
			region:   "eu-west-1",
			errCheck: require.NoError,
		},
		{
			name:     "us gov",
			region:   "us-gov-east-1",
			errCheck: require.NoError,
		},
		{
			name:     "valid format",
			region:   "xx-iso-somewhere-100",
			errCheck: require.NoError,
		},
		{
			name:     "empty",
			region:   "",
			errCheck: isBadParamErrFn,
		},
		{
			name:     "symbols",
			region:   "us@east-1",
			errCheck: isBadParamErrFn,
		},
		{
			name:     "invalid country code",
			region:   "xxx-east-1",
			errCheck: isBadParamErrFn,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			tt.errCheck(t, IsValidRegion(tt.region))
		})
	}
}

func TestCheckRoleARN(t *testing.T) {
	isBadParamErrFn := func(tt require.TestingT, err error, i ...any) {
		require.True(tt, trace.IsBadParameter(err), "expected bad parameter, got %v", err)
	}

	for _, tt := range []struct {
		name     string
		arn      string
		errCheck require.ErrorAssertionFunc
	}{
		{
			name:     "valid",
			arn:      "arn:aws:iam:us-west-2:123456789012:role/foo/bar",
			errCheck: require.NoError,
		},
		{
			name:     "empty string",
			arn:      "",
			errCheck: isBadParamErrFn,
		},
		{
			name:     "arn identifier but no other section",
			arn:      "arn:nil",
			errCheck: isBadParamErrFn,
		},
		{
			name:     "valid with resource that has spaces",
			arn:      "arn:aws:iam:us-west-2:123456789012:role/foo bar",
			errCheck: require.NoError,
		},
		{
			name:     "valid when resource section has :",
			arn:      "arn:aws:iam:us-west-2:123456789012:role/foo bar:a",
			errCheck: require.NoError,
		},
		{
			name:     "invalid when resource is missing",
			arn:      "arn:aws:iam:us-west-2:123456789012",
			errCheck: isBadParamErrFn,
		},
		{
			name:     "valid even if region is missing",
			arn:      "arn:aws:iam::123456789012:role/foo bar",
			errCheck: require.NoError,
		},
		{
			name:     "invalid when the resource is not role",
			arn:      "arn:aws:iam::123456789012:user/foo bar",
			errCheck: isBadParamErrFn,
		},
		{
			name:     "invalid when the resource is of type role, but role name section is missing",
			arn:      "arn:aws:iam::123456789012:role",
			errCheck: isBadParamErrFn,
		},
		{
			name:     "invalid when the resource is of type role, but role is empty",
			arn:      "arn:aws:iam::123456789012:role/",
			errCheck: isBadParamErrFn,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			tt.errCheck(t, CheckRoleARN(tt.arn))
		})
	}
}
