/*
Copyright The Kubernetes Authors.

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

// Code generated by client-gen. DO NOT EDIT.

package v1beta1

import (
	"context"

	v1beta1 "github.com/linkerd/linkerd2/controller/gen/apis/externalworkload/v1beta1"
	scheme "github.com/linkerd/linkerd2/controller/gen/client/clientset/versioned/scheme"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	types "k8s.io/apimachinery/pkg/types"
	watch "k8s.io/apimachinery/pkg/watch"
	gentype "k8s.io/client-go/gentype"
)

// ExternalWorkloadsGetter has a method to return a ExternalWorkloadInterface.
// A group's client should implement this interface.
type ExternalWorkloadsGetter interface {
	ExternalWorkloads(namespace string) ExternalWorkloadInterface
}

// ExternalWorkloadInterface has methods to work with ExternalWorkload resources.
type ExternalWorkloadInterface interface {
	Create(ctx context.Context, externalWorkload *v1beta1.ExternalWorkload, opts v1.CreateOptions) (*v1beta1.ExternalWorkload, error)
	Update(ctx context.Context, externalWorkload *v1beta1.ExternalWorkload, opts v1.UpdateOptions) (*v1beta1.ExternalWorkload, error)
	Delete(ctx context.Context, name string, opts v1.DeleteOptions) error
	DeleteCollection(ctx context.Context, opts v1.DeleteOptions, listOpts v1.ListOptions) error
	Get(ctx context.Context, name string, opts v1.GetOptions) (*v1beta1.ExternalWorkload, error)
	List(ctx context.Context, opts v1.ListOptions) (*v1beta1.ExternalWorkloadList, error)
	Watch(ctx context.Context, opts v1.ListOptions) (watch.Interface, error)
	Patch(ctx context.Context, name string, pt types.PatchType, data []byte, opts v1.PatchOptions, subresources ...string) (result *v1beta1.ExternalWorkload, err error)
	ExternalWorkloadExpansion
}

// externalWorkloads implements ExternalWorkloadInterface
type externalWorkloads struct {
	*gentype.ClientWithList[*v1beta1.ExternalWorkload, *v1beta1.ExternalWorkloadList]
}

// newExternalWorkloads returns a ExternalWorkloads
func newExternalWorkloads(c *ExternalworkloadV1beta1Client, namespace string) *externalWorkloads {
	return &externalWorkloads{
		gentype.NewClientWithList[*v1beta1.ExternalWorkload, *v1beta1.ExternalWorkloadList](
			"externalworkloads",
			c.RESTClient(),
			scheme.ParameterCodec,
			namespace,
			func() *v1beta1.ExternalWorkload { return &v1beta1.ExternalWorkload{} },
			func() *v1beta1.ExternalWorkloadList { return &v1beta1.ExternalWorkloadList{} }),
	}
}
