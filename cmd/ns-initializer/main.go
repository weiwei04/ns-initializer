package main

import (
	"flag"

	"github.com/golang/glog"
	"github.com/weiwei04/ns-initializer/pkg/controller"
)

var (
	quotaFile  = flag.String("quota", "/var/lib/ns-initializer/quota/namespace-quota.yaml", "quota")
	limitFile  = flag.String("limit", "/var/lib/ns-initializer/limits/namespace-limits.yaml", "limit")
	secretFile = flag.String("secret", "/var/lib/ns-initializer/secrets/namespace-secrets.yaml", "secret")
	//saFile = flag.String("sa", "/var/lib/ns-initializer/serviceaccount/serviceaccount.yaml", "service account")
)

func main() {
	flag.Parse()
	glog.Info("quotaFile:", *quotaFile)
	glog.Infof("limitFile:", *limitFile)
	ctrl := controller.New(controller.Config{
		ResourceQuota: *quotaFile,
		LimitRange:    *limitFile,
	})
	stopCh := make(chan struct{})
	glog.Infof("start to run controller")
	ctrl.Run(stopCh)
}
