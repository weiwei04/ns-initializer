/*
Copyright 2015 The Kubernetes Authors.

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
	"fmt"
	"time"

	"k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	meta_v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	coreinformers "k8s.io/client-go/informers/core/v1"
	clientset "k8s.io/client-go/kubernetes"
	corelisters "k8s.io/client-go/listers/core/v1"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/workqueue"
	//"k8s.io/kubernetes/pkg/util/metrics"
	//"k8s.io/kubernetes/pkg/controller"
	//"k8s.io/kubernetes/pkg/controller/namespace/deletion"
	//"k8s.io/kubernetes/pkg/util/metrics"
	"github.com/golang/glog"
	corev1 "k8s.io/client-go/kubernetes/typed/core/v1"
)

const (
	// namespaceDeletionGracePeriod is the time period to wait before processing a received namespace event.
	// This allows time for the following to occur:
	// * lifecycle admission plugins on HA apiservers to also observe a namespace
	//   deletion and prevent new objects from being created in the terminating namespace
	// * non-leader etcd servers to observe last-minute object creations in a namespace
	//   so this controller's cleanup can actually clean up all objects
	namespaceDeletionGracePeriod = 5 * time.Second
)

// NamespaceController is responsible for performing actions dependent upon a namespace phase
type NSInitializer struct {
	// the default ResourceQuota should enforced to new namespaces
	quota *v1.ResourceQuota
	// the default LimitRanges shoud enforced to new namespaces
	limits *v1.LimitRange
	//
	client corev1.CoreV1Interface
	// lister that can list namespaces from a shared cache
	lister corelisters.NamespaceLister
	// returns true when the namespace cache is ready
	listerSynced cache.InformerSynced
	// namespaces that have been queued up for processing by workers
	queue workqueue.RateLimitingInterface
}

// NewNamespaceController creates a new NamespaceController
func NewNSInitializer(
	quota *v1.ResourceQuota,
	limits *v1.LimitRange,
	kubeClient clientset.Interface,
	namespaceInformer coreinformers.NamespaceInformer,
	resyncPeriod time.Duration) *NSInitializer {

	// create the controller so we can inject the enqueue function
	namespaceController := &NSInitializer{
		quota:  quota,
		limits: limits,
		client: kubeClient.CoreV1(),
		queue:  workqueue.NewNamedRateLimitingQueue(workqueue.DefaultControllerRateLimiter(), "ns-initializer"),
	}

	//if kubeClient != nil && kubeClient.Core().RESTClient().GetRateLimiter() != nil {
	//	metrics.RegisterMetricAndTrackRateLimiterUsage("ns-initializer", kubeClient.Core().RESTClient().GetRateLimiter())
	//}

	// configure the namespace informer event handlers
	namespaceInformer.Informer().AddEventHandlerWithResyncPeriod(
		cache.ResourceEventHandlerFuncs{
			AddFunc: func(obj interface{}) {
				namespace := obj.(*v1.Namespace)
				namespaceController.enqueueNamespace(namespace)
			},
			UpdateFunc: func(oldObj, newObj interface{}) {
				namespace := newObj.(*v1.Namespace)
				namespaceController.enqueueNamespace(namespace)
			},
		},
		resyncPeriod,
	)
	namespaceController.lister = namespaceInformer.Lister()
	namespaceController.listerSynced = namespaceInformer.Informer().HasSynced

	return namespaceController
}

// enqueueNamespace adds an object to the controller work queue
// obj could be an *v1.Namespace, or a DeletionFinalStateUnknown item.
func (nm *NSInitializer) enqueueNamespace(ns *v1.Namespace) {
	// don't queue if we aren going to delete
	if ns.DeletionTimestamp != nil || !ns.DeletionTimestamp.IsZero() {
		glog.V(4).Infof("namespace %s going to delete will ignore", ns.Name)
		return
	}

	if ns.Name == "kube-system" || ns.Name == "kube-public" {
		glog.Infof("skip system namespace: %s", ns.Name)
		return
	}

	if _, ok := ns.Annotations["vip"]; ok {
		glog.Infof("skip vip namespace: %s", ns.Name)
		return
	}

	if _, ok := ns.Annotations["sealed"]; ok {
		glog.Infof("skip sealed namespace: %s", ns.Name)
		return
	}

	nm.queue.Add(ns.Name)
}

// worker processes the queue of namespace objects.
// Each namespace can be in the queue at most once.
// The system ensures that no two workers can process
// the same namespace at the same time.
func (nm *NSInitializer) worker() {
	workFunc := func() bool {
		key, quit := nm.queue.Get()
		if quit {
			return true
		}
		defer nm.queue.Done(key)

		err := nm.syncNamespaceFromKey(key.(string))
		if err == nil {
			// no error, forget this entry and return
			nm.queue.Forget(key)
		} else {
			// rather than wait for a full resync, re-add the namespace to the queue to be processed
			nm.queue.AddRateLimited(key)
			utilruntime.HandleError(err)
		}
		return false
	}

	for !workFunc() {
	}
}

// syncNamespaceFromKey looks for a namespace with the specified key in its store and synchronizes it
func (nm *NSInitializer) syncNamespaceFromKey(key string) error {
	var (
		err       error
		namespace *v1.Namespace
	)
	glog.Infof("syncNamespaceFromKey(%s)", key)
	startTime := time.Now()
	defer func() {
		if err == nil {
			glog.Infof("Finished syncing namespace %q (%v)", key, time.Now().Sub(startTime))
		} else {
			glog.Errorf("syncing namespace %s, failed with err:%s", key, err)
		}
	}()

	namespace, err = nm.lister.Get(key)
	if errors.IsNotFound(err) {
		glog.Infof("Namespace has been deleted %v", key)
		return nil
	}
	if err != nil {
		glog.Errorf("Unable to retrieve namespace %s from store: %v", key, err)
		utilruntime.HandleError(fmt.Errorf("Unable to retrieve namespace %v from store: %v", key, err))
		return err
	}
	if _, ok := namespace.Annotations["vip"]; ok {
		glog.Infof("vip namespace will not enforce namespace quota & limits")
		return nil
	}
	if _, ok := namespace.Annotations["sealed"]; ok {
		glog.Infof("namespace %s already sealed", key)
		return nil
	}
	// enforce resource quota & limit range
	err = nm.sealNamespace(namespace)
	return err
}

func (nm *NSInitializer) sealNamespace(ns *v1.Namespace) error {
	var err error
	err = nm.enforceNamespaceResourceQuota(ns)
	if err != nil {
		return err
	}
	glog.Infof("enforcedNamespaceResourceQuota on %s", ns.Name)
	err = nm.enforceNamespaceLimitRange(ns)
	if err != nil {
		return err
	}
	glog.Infof("enforceNamespaceLimitRange on %s", ns.Name)
	// make a copy of ns and annotate the namespace
	return nil
}
func (nm *NSInitializer) enforceNamespaceResourceQuota(ns *v1.Namespace) error {
	quota, err := nm.client.ResourceQuotas(ns.Name).Get("kirk-default-quota", meta_v1.GetOptions{})
	if errors.IsNotFound(err) {
		_, err = nm.client.ResourceQuotas(ns.Name).Create(nm.quota)
		if err != nil {
			glog.Errorf("Create ResourceQuotas for namespace:%s failed, err:%s", ns.Name, err)
		}
		return err
	} else if err != nil {
		glog.Errorf("Get namespace: %s resourcequota: kirk-default-limit failed with err:%#v",
			ns.Name, err)
	}
	glog.Infof("skip Namespace: %s with ResourceQuota: %#v", quota)
	return nil
}

func (nm *NSInitializer) enforceNamespaceLimitRange(ns *v1.Namespace) error {
	limit, err := nm.client.LimitRanges(ns.Name).Get("kirk-default-limit", meta_v1.GetOptions{})
	if errors.IsNotFound(err) {
		_, err = nm.client.LimitRanges(ns.Name).Create(nm.limits)
		if err != nil {
			glog.Errorf("Create LimitRange for namespace:%s failed, err:%s", ns.Name, err)
		}
		return err
	} else if err != nil {
		glog.Errorf("Get namespace: %s limitrange: kirk-default-limit failed with err:%#v",
			ns.Name, err)
	}
	glog.Infof("skip namespace: %s with limitrange:%#v", ns.Name, limit)
	return nil
}

func waitForCacheSync(controllerName string, stopCh <-chan struct{}, cacheSyncs ...cache.InformerSynced) bool {
	glog.Infof("Waiting for caches to sync for %s controller", controllerName)

	if !cache.WaitForCacheSync(stopCh, cacheSyncs...) {
		utilruntime.HandleError(fmt.Errorf("Unable to sync caches for %s controller", controllerName))
		return false
	}

	glog.Infof("Caches are synced for %s controller", controllerName)
	return true
}

// Run starts observing the system with the specified number of workers.
func (nm *NSInitializer) Run(workers int, stopCh <-chan struct{}) {
	defer utilruntime.HandleCrash()
	defer nm.queue.ShutDown()

	if !waitForCacheSync("namespace", stopCh, nm.listerSynced) {
		return
	}

	glog.Info("Starting workers")
	for i := 0; i < workers; i++ {
		go wait.Until(nm.worker, time.Second, stopCh)
	}
	<-stopCh
	glog.V(1).Infof("Shutting down")
}
