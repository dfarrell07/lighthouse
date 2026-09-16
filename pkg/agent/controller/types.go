/*
SPDX-License-Identifier: Apache-2.0

Copyright Contributors to the Submariner project.

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

package controller

import (
	"sync"
	"time"

	"github.com/submariner-io/admiral/pkg/federate"
	"github.com/submariner-io/admiral/pkg/ipam"
	"github.com/submariner-io/admiral/pkg/syncer"
	"github.com/submariner-io/admiral/pkg/syncer/broker"
	"github.com/submariner-io/admiral/pkg/watcher"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	k8slabels "k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/tools/cache"
	mcsv1a1 "sigs.k8s.io/mcs-api/pkg/apis/v1alpha1"
)

const (
	ServiceExportReasonNoServiceImport                mcsv1a1.ServiceExportConditionReason = "NoServiceImport"
	ServiceExportReasonRetrievalFailed                mcsv1a1.ServiceExportConditionReason = "ServiceRetrievalFailed"
	ServiceExportReasonClusterSetIPEnablementConflict mcsv1a1.ServiceExportConditionReason = "ClusterSetIPEnablementConflict"
	ServiceExportReasonUnsupportedIPFamily            mcsv1a1.ServiceExportConditionReason = "UnsupportedIPFamily"
	ServiceExportReasonGlobalIPUnavailable            mcsv1a1.ServiceExportConditionReason = "ServiceGlobalIPUnavailable"
	ServiceExportReasonRestrictedNamespace            mcsv1a1.ServiceExportConditionReason = "RestrictedNamespace"
)

type EndpointSliceListerFn func(selector k8slabels.Selector) []runtime.Object

type AggregatedServiceImportGetterFn func(serviceName string, serviceNamespace string) *mcsv1a1.ServiceImport

type converter struct {
	scheme *runtime.Scheme
}

type Controller struct {
	serviceExportSyncer         syncer.Interface
	serviceSyncer               syncer.Interface
	localServiceImportFederator federate.Federator
	namespaceInformer           cache.SharedInformer
	serviceExportClient         *ServiceExportClient
	endpointSliceController     *EndpointSliceController
	serviceImportController     *ServiceImportController
	namespaceValidator          *NamespaceValidator
	clusterID                   string
	namespace                   string
	globalnetEnabled            bool
}

type AgentSpecification struct {
	ClusterID           string
	Namespace           string
	ClustersetIPCidr    string `split_words:"true"`
	ClustersetIPEnabled bool   `split_words:"true"`
	GlobalnetEnabled    bool   `split_words:"true"`
	Uninstall           bool
	HaltOnCertError     bool `split_words:"true"`
}

// The ServiceImportController encapsulates two resource syncers; one that watches for local cluster ServiceImports
// from the submariner namespace and creates/updates the aggregated ServiceImport on the broker; the other that syncs
// aggregated ServiceImports from the broker to the local service namespace. It also creates a ServiceEndpointSliceController.
type ServiceImportController struct {
	remoteSyncer               syncer.Interface
	brokerClient               dynamic.Interface
	localClient                dynamic.Interface
	restMapper                 meta.RESTMapper
	localFederator             federate.Federator
	localSyncer                syncer.Interface
	globalIngressIPCache       *globalIngressIPCache
	serviceExportClient        *ServiceExportClient
	converter                  converter
	localLHEndpointSliceLister EndpointSliceListerFn
	clustersetIPPool           *ipam.IPPool
	namespaceValidator         *NamespaceValidator
	endpointControllers        sync.Map
	clusterID                  string
	localNamespace             string
	brokerNamespace            string
	clustersetIPEnabled        bool
}

// Each ServiceEndpointSliceController watches for the EndpointSlices that backs a Service and have a ServiceImport.
// It creates LH EndpointSlices corresponding to service EndpointSlices that are distributed to other clusters.
type ServiceEndpointSliceController struct {
	localClient              dynamic.ResourceInterface
	ingressIPClient          dynamic.NamespaceableResourceInterface
	epsSyncer                syncer.Interface
	serviceImportSpec        *mcsv1a1.ServiceImportSpec
	stopCh                   chan struct{}
	globalIngressIPCache     *globalIngressIPCache
	clusterID                string
	serviceName              string
	serviceNamespace         string
	publishNotReadyAddresses string
	awaitStoppedTimeout      time.Duration
	stopOnce                 sync.Once
}

// EndpointSliceController encapsulates a syncer that syncs EndpointSlices to and from that broker.
type EndpointSliceController struct {
	serviceSyncer       syncer.Interface
	localClient         dynamic.Interface
	syncer              *broker.Syncer
	serviceExportClient *ServiceExportClient
	namespaceValidator  *NamespaceValidator
	clusterID           string
}

type ServiceExportClient struct {
	dynamic.NamespaceableResourceInterface
	converter
	localSyncer syncer.Interface
}

type globalIngressIPEntry struct {
	obj           *unstructured.Unstructured
	onAddOrUpdate func()
}

type globalIngressIPTransformFn func(obj *unstructured.Unstructured) (any, bool)

type globalIngressIPMap struct {
	entries map[string]*globalIngressIPEntry
	sync.Mutex
}

type globalIngressIPCache struct {
	watcher     watcher.Interface
	byService   globalIngressIPMap
	byPod       globalIngressIPMap
	byEndpoints globalIngressIPMap
}
