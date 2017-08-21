package controller

import (
	"bytes"
	"time"

	"io/ioutil"

	"github.com/golang/glog"
	"k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/yaml"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

type Config struct {
	ResourceQuota string
	LimitRange    string
}

type Controller struct {
	config      Config
	initializer *NSInitializer
}

func New(config Config) *Controller {
	return &Controller{config: config}
}

func loadFromSpec(fileName string, obj interface{}) (interface{}, error) {
	data, err := ioutil.ReadFile(fileName)
	if err != nil {
		return nil, err
	}
	return obj, yaml.NewYAMLToJSONDecoder(bytes.NewBuffer(data)).Decode(obj)
}

func (c *Controller) Run(stopCh <-chan struct{}) error {
	quota := &v1.ResourceQuota{}
	_, err := loadFromSpec(c.config.ResourceQuota, quota)
	if err != nil {
		glog.Errorf("Load resourcequota from file:%s failed err:%v",
			c.config.ResourceQuota, err)
		return err
	}
	glog.V(1).Info("ke-quota:")
	glog.V(1).Infof("%v", quota)
	limit := &v1.LimitRange{}
	_, err = loadFromSpec(c.config.LimitRange, limit)
	if err != nil {
		glog.Errorf("Load limitrange from file:%s failed err:%v",
			c.config.LimitRange, err)
		return err
	}
	glog.V(1).Info("ke-limit:")
	glog.V(1).Infof("%v", limit)
	config, err := rest.InClusterConfig()
	if err != nil {
		glog.Errorf("Get InClusterConfig failed err:%s", err)
		return err
	}
	// creates the clientset
	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		glog.Errorf("New clientset failed err:%s", err)
		return err
	}
	informerFactory := informers.NewSharedInformerFactory(clientset, time.Second*10)
	c.initializer = NewNSInitializer(
		quota,
		limit,
		clientset,
		informerFactory.Core().V1().Namespaces(),
		time.Minute*5)
	informerFactory.Start(stopCh)
	c.initializer.Run(1, stopCh)
	return nil
}
