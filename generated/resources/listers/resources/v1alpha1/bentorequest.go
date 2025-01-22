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
// Code generated by lister-gen. DO NOT EDIT.

package v1alpha1

import (
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/client-go/tools/cache"

	v1alpha1 "github.com/bentoml/yatai-image-builder/v2/apis/resources/v1alpha1"
)

// BentoRequestLister helps list BentoRequests.
// All objects returned here must be treated as read-only.
type BentoRequestLister interface {
	// List lists all BentoRequests in the indexer.
	// Objects returned here must be treated as read-only.
	List(selector labels.Selector) (ret []*v1alpha1.BentoRequest, err error)
	// BentoRequests returns an object that can list and get BentoRequests.
	BentoRequests(namespace string) BentoRequestNamespaceLister
	BentoRequestListerExpansion
}

// bentoRequestLister implements the BentoRequestLister interface.
type bentoRequestLister struct {
	indexer cache.Indexer
}

// NewBentoRequestLister returns a new BentoRequestLister.
func NewBentoRequestLister(indexer cache.Indexer) BentoRequestLister {
	return &bentoRequestLister{indexer: indexer}
}

// List lists all BentoRequests in the indexer.
func (s *bentoRequestLister) List(selector labels.Selector) (ret []*v1alpha1.BentoRequest, err error) {
	err = cache.ListAll(s.indexer, selector, func(m interface{}) {
		ret = append(ret, m.(*v1alpha1.BentoRequest))
	})
	return ret, err
}

// BentoRequests returns an object that can list and get BentoRequests.
func (s *bentoRequestLister) BentoRequests(namespace string) BentoRequestNamespaceLister {
	return bentoRequestNamespaceLister{indexer: s.indexer, namespace: namespace}
}

// BentoRequestNamespaceLister helps list and get BentoRequests.
// All objects returned here must be treated as read-only.
type BentoRequestNamespaceLister interface {
	// List lists all BentoRequests in the indexer for a given namespace.
	// Objects returned here must be treated as read-only.
	List(selector labels.Selector) (ret []*v1alpha1.BentoRequest, err error)
	// Get retrieves the BentoRequest from the indexer for a given namespace and name.
	// Objects returned here must be treated as read-only.
	Get(name string) (*v1alpha1.BentoRequest, error)
	BentoRequestNamespaceListerExpansion
}

// bentoRequestNamespaceLister implements the BentoRequestNamespaceLister
// interface.
type bentoRequestNamespaceLister struct {
	indexer   cache.Indexer
	namespace string
}

// List lists all BentoRequests in the indexer for a given namespace.
func (s bentoRequestNamespaceLister) List(selector labels.Selector) (ret []*v1alpha1.BentoRequest, err error) {
	err = cache.ListAllByNamespace(s.indexer, s.namespace, selector, func(m interface{}) {
		ret = append(ret, m.(*v1alpha1.BentoRequest))
	})
	return ret, err
}

// Get retrieves the BentoRequest from the indexer for a given namespace and name.
func (s bentoRequestNamespaceLister) Get(name string) (*v1alpha1.BentoRequest, error) {
	obj, exists, err := s.indexer.GetByKey(s.namespace + "/" + name)
	if err != nil {
		return nil, err
	}
	if !exists {
		return nil, errors.NewNotFound(v1alpha1.Resource("bentorequest"), name)
	}
	return obj.(*v1alpha1.BentoRequest), nil
}
