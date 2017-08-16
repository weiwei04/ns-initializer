package controller

import (
	"bytes"
	"time"

	"io/ioutil"

	//yaml "gopkg.in/yaml.v2"

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

func loadResourceQuota(fileName string) (*v1.ResourceQuota, error) {
	data, err := ioutil.ReadFile(fileName)
	if err != nil {
		return nil, err
	}
	quota := &v1.ResourceQuota{}
	r := bytes.NewBuffer(data)
	return quota, yaml.NewYAMLToJSONDecoder(r).Decode(quota)
}

func loadLimitRange(fileName string) (*v1.LimitRange, error) {
	data, err := ioutil.ReadFile(fileName)
	if err != nil {
		return nil, err
	}
	limit := &v1.LimitRange{}
	r := bytes.NewBuffer(data)
	return limit, yaml.NewYAMLToJSONDecoder(r).Decode(limit)
}

func (c *Controller) Run(stopCh <-chan struct{}) error {
	quota, err := loadResourceQuota(c.config.ResourceQuota)
	if err != nil {
		glog.Errorf("loadResourceQuota from %s failed, err:%s",
			c.config.ResourceQuota, err)
		return err
	}
	glog.Infof("loaded resouce quota file:")
	glog.Infof("%#v", quota)
	limit, err := loadLimitRange(c.config.LimitRange)
	if err != nil {
		glog.Errorf("loadLimitRange from %s failed, err:%s",
			c.config.LimitRange, err)
		return err
	}
	glog.Infof("loaded limitrange file:")
	glog.Infof("%#v", limit)
	config, err := rest.InClusterConfig()
	if err != nil {
		glog.Errorf("client-go get InClusterConfig failed, err:%s", err)
		return err
	}
	glog.Infof("get in cluster config")
	// creates the clientset
	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		glog.Errorf("new clientset failed, err:%s", err)
		return err
	}
	glog.Infof("create clientset from in cluster config")
	informerFactory := informers.NewSharedInformerFactory(clientset, time.Second*10)
	c.initializer = NewNSInitializer(
		quota,
		limit,
		clientset,
		informerFactory.Core().V1().Namespaces(),
		time.Second*10)
	glog.Infof("NewNSInitializer, try to run initializer")
	informerFactory.Start(stopCh)
	c.initializer.Run(1, stopCh)
	return nil
}
