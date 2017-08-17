package controller

import (
	"fmt"
	"strings"
	"time"

	"k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	meta_v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	coreinformers "k8s.io/client-go/informers/core/v1"
	clientset "k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/scheme"
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
	quotaName string = "ke-quota"
	limitName string = "ke-limit"
)

const (
	vipAnnotation   = "vip"
	readyAnnotation = "ready"
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
		glog.V(3).Infof("Namespace:%s going to delete will ignore", ns.Name)
		return
	}

	if strings.HasPrefix(ns.Name, "kube") || strings.HasPrefix(ns.Name, "e2e") {
		glog.V(3).Infof("Skip dedicated namespace:%s", ns.Name)
		return
	}

	if _, ok := ns.Annotations[vipAnnotation]; ok {
		glog.V(3).Infof("Skip vip namespace:%s", ns.Name)
		return
	}

	if _, ok := ns.Annotations[readyAnnotation]; ok {
		glog.V(3).Infof("Skip initialized namespace:%s", ns.Name)
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
	glog.Infof("Sync namespace:%s", key)

	var (
		err       error
		namespace *v1.Namespace
	)
	startTime := time.Now()
	defer func() {
		if err == nil {
			glog.Infof("Finished syncing namespace:%s time_cost:%v", key, time.Now().Sub(startTime))
		} else {
			glog.Errorf("Faied syncing namespace:%s err:%s", key, err)
		}
	}()

	namespace, err = nm.lister.Get(key)
	if errors.IsNotFound(err) {
		glog.V(1).Infof("Namespace:%s has been deleted", key)
		return nil
	}
	if err != nil {
		glog.Errorf("Unable to retrieve namespace:%s from store err:%v", key, err)
		utilruntime.HandleError(fmt.Errorf("Unable to retrieve namespace:%v from store err:%v", key, err))
		return err
	}
	// enforce resource quota & limit range
	err = nm.initNamesapce(namespace)
	return err
}

func (nm *NSInitializer) initNamesapce(ns *v1.Namespace) error {
	var err error
	err = nm.enforceNamespaceResourceQuota(ns)
	if err != nil {
		return err
	}
	err = nm.enforceNamespaceLimitRange(ns)
	if err != nil {
		return err
	}

	copy, err := scheme.Scheme.DeepCopy(ns)
	if err != nil {
		glog.Errorf("Copy namespace:%s obj:%v failed err:%v", ns.Name, ns, err)
		return err
	}
	ns = copy.(*v1.Namespace)
	if ns.Annotations == nil {
		ns.Annotations = make(map[string]string)
	}
	ns.Annotations[readyAnnotation] = "ready"
	_, err = nm.client.Namespaces().Update(ns)
	if err != nil {
		glog.Errorf("Mark namespace:%s as ready failed err:%v", ns.Name, err)
	}
	return err
}

func (nm *NSInitializer) enforceNamespaceResourceQuota(ns *v1.Namespace) error {
	var err error
	_, err = nm.client.ResourceQuotas(ns.Name).Get(quotaName, meta_v1.GetOptions{})
	if errors.IsNotFound(err) {
		_, err = nm.client.ResourceQuotas(ns.Name).Create(nm.quota)
		if err != nil {
			glog.Errorf("Create namespace:%s resourcequota:%s failed err:%v",
				ns.Name, nm.quota, err)
		}
	} else if err != nil {
		glog.Errorf("Get namespace:%s resourcequota:%s failed err:%v",
			ns.Name, quotaName, err)
	} else {
		glog.V(2).Infof("Namespace:%s already have resourcequota:%s",
			ns.Name, quotaName)
	}
	return err
}

func (nm *NSInitializer) enforceNamespaceLimitRange(ns *v1.Namespace) error {
	var err error
	_, err = nm.client.LimitRanges(ns.Name).Get(limitName, meta_v1.GetOptions{})
	if errors.IsNotFound(err) {
		_, err = nm.client.LimitRanges(ns.Name).Create(nm.limits)
		if err != nil {
			glog.Errorf("Create namespace:%s limitranges:%s failed err:%#v",
				ns.Name, limitName, err)
		}
	} else if err != nil {
		glog.Errorf("Get namespace:%s limitranges:%s failed err:%s",
			ns.Name, limitName, err)
	} else {
		glog.V(2).Infof("Namespace:%s already have limitranges:%s",
			ns.Name, limitName)
	}
	return err
}

func waitForCacheSync(controllerName string, stopCh <-chan struct{}, cacheSyncs ...cache.InformerSynced) bool {
	glog.V(4).Infof("Waiting for caches to sync for controller:%s", controllerName)

	if !cache.WaitForCacheSync(stopCh, cacheSyncs...) {
		utilruntime.HandleError(fmt.Errorf("Unable to sync caches for %s controller", controllerName))
		return false
	}

	glog.Infof("Caches are synced for controller:%s", controllerName)
	return true
}

// Run starts observing the system with the specified number of workers.
func (nm *NSInitializer) Run(workers int, stopCh <-chan struct{}) {
	defer utilruntime.HandleCrash()
	defer nm.queue.ShutDown()

	if !waitForCacheSync("ns-initializer", stopCh, nm.listerSynced) {
		return
	}

	for i := 0; i < workers; i++ {
		glog.Info("Starting workers:%d", i)
		go wait.Until(nm.worker, time.Second, stopCh)
	}
	<-stopCh
	glog.V(1).Infof("Shutting down")
}
