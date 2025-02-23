// Copyright 2023 Gravitational, Inc
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package teleport

import (
	"context"

	"github.com/gravitational/teleport/api/client/proto"
	"github.com/gravitational/teleport/api/types"
	"github.com/gravitational/teleport/integrations/lib/plugindata"
)

// Client aggregates the parts of Teleport API client interface
// (as implemented by github.com/gravitational/teleport/api/client.Client)
// that are used by the access plugins.
type Client interface {
	plugindata.Client
	types.Events
	Ping(context.Context) (proto.PingResponse, error)
	GetAccessRequests(ctx context.Context, filter types.AccessRequestFilter) ([]types.AccessRequest, error)
	SubmitAccessReview(ctx context.Context, params types.AccessReviewSubmission) (types.AccessRequest, error)
	SetAccessRequestState(ctx context.Context, params types.AccessRequestUpdate) error
	ListResources(ctx context.Context, req proto.ListResourcesRequest) (*types.ListResourcesResponse, error)
}
