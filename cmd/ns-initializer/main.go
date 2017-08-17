package main

import (
	"flag"
	"os"
	"os/signal"
	"syscall"

	"github.com/golang/glog"
	"github.com/weiwei04/ns-initializer/pkg/controller"
)

var (
	quotaFile = flag.String("quota", "/etc/ns-initializer/quota/ke-quota.yaml", "quota")
	limitFile = flag.String("limit", "/etc/ns-initializer/limit/ke-limit.yaml", "limit")
)

// TODO: serve http health check
func main() {
	flag.Parse()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGTERM)
	signal.Notify(sigCh, syscall.SIGINT)
	stopCh := make(chan struct{})
	go func() {
		glog.Infof("ns-initializer received %s will stop", <-sigCh)
		close(stopCh)
	}()

	ctrl := controller.New(controller.Config{
		ResourceQuota: *quotaFile,
		LimitRange:    *limitFile,
	})
	glog.Infof("ns-initializer start to run...")
	ctrl.Run(stopCh)
	glog.Infof("ns-initializer stopped")
}
