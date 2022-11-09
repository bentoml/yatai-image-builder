/*
Copyright 2022.

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

package v1alpha1

import (
	"context"
	"time"

	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	types "k8s.io/apimachinery/pkg/types"
	watch "k8s.io/apimachinery/pkg/watch"
	rest "k8s.io/client-go/rest"

	v1alpha1 "github.com/bentoml/yatai-image-builder/apis/resources/v1alpha1"
	scheme "github.com/bentoml/yatai-image-builder/generated/resources/clientset/versioned/scheme"
)

// BentosGetter has a method to return a BentoInterface.
// A group's client should implement this interface.
type BentosGetter interface {
	Bentos(namespace string) BentoInterface
}

// BentoInterface has methods to work with Bento resources.
type BentoInterface interface {
	Create(ctx context.Context, bento *v1alpha1.Bento, opts v1.CreateOptions) (*v1alpha1.Bento, error)
	Update(ctx context.Context, bento *v1alpha1.Bento, opts v1.UpdateOptions) (*v1alpha1.Bento, error)
	UpdateStatus(ctx context.Context, bento *v1alpha1.Bento, opts v1.UpdateOptions) (*v1alpha1.Bento, error)
	Delete(ctx context.Context, name string, opts v1.DeleteOptions) error
	DeleteCollection(ctx context.Context, opts v1.DeleteOptions, listOpts v1.ListOptions) error
	Get(ctx context.Context, name string, opts v1.GetOptions) (*v1alpha1.Bento, error)
	List(ctx context.Context, opts v1.ListOptions) (*v1alpha1.BentoList, error)
	Watch(ctx context.Context, opts v1.ListOptions) (watch.Interface, error)
	Patch(ctx context.Context, name string, pt types.PatchType, data []byte, opts v1.PatchOptions, subresources ...string) (result *v1alpha1.Bento, err error)
	BentoExpansion
}

// bentos implements BentoInterface
type bentos struct {
	client rest.Interface
	ns     string
}

// newBentos returns a Bentos
func newBentos(c *ResourcesV1alpha1Client, namespace string) *bentos {
	return &bentos{
		client: c.RESTClient(),
		ns:     namespace,
	}
}

// Get takes name of the bento, and returns the corresponding bento object, and an error if there is any.
func (c *bentos) Get(ctx context.Context, name string, options v1.GetOptions) (result *v1alpha1.Bento, err error) {
	result = &v1alpha1.Bento{}
	err = c.client.Get().
		Namespace(c.ns).
		Resource("bentos").
		Name(name).
		VersionedParams(&options, scheme.ParameterCodec).
		Do(ctx).
		Into(result)
	return
}

// List takes label and field selectors, and returns the list of Bentos that match those selectors.
func (c *bentos) List(ctx context.Context, opts v1.ListOptions) (result *v1alpha1.BentoList, err error) {
	var timeout time.Duration
	if opts.TimeoutSeconds != nil {
		timeout = time.Duration(*opts.TimeoutSeconds) * time.Second
	}
	result = &v1alpha1.BentoList{}
	err = c.client.Get().
		Namespace(c.ns).
		Resource("bentos").
		VersionedParams(&opts, scheme.ParameterCodec).
		Timeout(timeout).
		Do(ctx).
		Into(result)
	return
}

// Watch returns a watch.Interface that watches the requested bentos.
func (c *bentos) Watch(ctx context.Context, opts v1.ListOptions) (watch.Interface, error) {
	var timeout time.Duration
	if opts.TimeoutSeconds != nil {
		timeout = time.Duration(*opts.TimeoutSeconds) * time.Second
	}
	opts.Watch = true
	return c.client.Get().
		Namespace(c.ns).
		Resource("bentos").
		VersionedParams(&opts, scheme.ParameterCodec).
		Timeout(timeout).
		Watch(ctx)
}

// Create takes the representation of a bento and creates it.  Returns the server's representation of the bento, and an error, if there is any.
func (c *bentos) Create(ctx context.Context, bento *v1alpha1.Bento, opts v1.CreateOptions) (result *v1alpha1.Bento, err error) {
	result = &v1alpha1.Bento{}
	err = c.client.Post().
		Namespace(c.ns).
		Resource("bentos").
		VersionedParams(&opts, scheme.ParameterCodec).
		Body(bento).
		Do(ctx).
		Into(result)
	return
}

// Update takes the representation of a bento and updates it. Returns the server's representation of the bento, and an error, if there is any.
func (c *bentos) Update(ctx context.Context, bento *v1alpha1.Bento, opts v1.UpdateOptions) (result *v1alpha1.Bento, err error) {
	result = &v1alpha1.Bento{}
	err = c.client.Put().
		Namespace(c.ns).
		Resource("bentos").
		Name(bento.Name).
		VersionedParams(&opts, scheme.ParameterCodec).
		Body(bento).
		Do(ctx).
		Into(result)
	return
}

// UpdateStatus was generated because the type contains a Status member.
// Add a +genclient:noStatus comment above the type to avoid generating UpdateStatus().
func (c *bentos) UpdateStatus(ctx context.Context, bento *v1alpha1.Bento, opts v1.UpdateOptions) (result *v1alpha1.Bento, err error) {
	result = &v1alpha1.Bento{}
	err = c.client.Put().
		Namespace(c.ns).
		Resource("bentos").
		Name(bento.Name).
		SubResource("status").
		VersionedParams(&opts, scheme.ParameterCodec).
		Body(bento).
		Do(ctx).
		Into(result)
	return
}

// Delete takes name of the bento and deletes it. Returns an error if one occurs.
func (c *bentos) Delete(ctx context.Context, name string, opts v1.DeleteOptions) error {
	return c.client.Delete().
		Namespace(c.ns).
		Resource("bentos").
		Name(name).
		Body(&opts).
		Do(ctx).
		Error()
}

// DeleteCollection deletes a collection of objects.
func (c *bentos) DeleteCollection(ctx context.Context, opts v1.DeleteOptions, listOpts v1.ListOptions) error {
	var timeout time.Duration
	if listOpts.TimeoutSeconds != nil {
		timeout = time.Duration(*listOpts.TimeoutSeconds) * time.Second
	}
	return c.client.Delete().
		Namespace(c.ns).
		Resource("bentos").
		VersionedParams(&listOpts, scheme.ParameterCodec).
		Timeout(timeout).
		Body(&opts).
		Do(ctx).
		Error()
}

// Patch applies the patch and returns the patched bento.
func (c *bentos) Patch(ctx context.Context, name string, pt types.PatchType, data []byte, opts v1.PatchOptions, subresources ...string) (result *v1alpha1.Bento, err error) {
	result = &v1alpha1.Bento{}
	err = c.client.Patch(pt).
		Namespace(c.ns).
		Resource("bentos").
		Name(name).
		SubResource(subresources...).
		VersionedParams(&opts, scheme.ParameterCodec).
		Body(data).
		Do(ctx).
		Into(result)
	return
}
