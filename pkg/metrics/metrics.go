// SPDX-License-Identifier: Apache-2.0
// Copyright Authors of Tetragon

package metrics

import (
	"net/http"
	"sync"
	"time"

	"github.com/cilium/tetragon/pkg/grpc/tracing"
	"github.com/cilium/tetragon/pkg/logger"
	"github.com/cilium/tetragon/pkg/metrics/errormetrics"
	"github.com/cilium/tetragon/pkg/metrics/eventcachemetrics"
	"github.com/cilium/tetragon/pkg/metrics/eventmetrics"
	"github.com/cilium/tetragon/pkg/metrics/kprobemetrics"
	"github.com/cilium/tetragon/pkg/metrics/mapmetrics"
	"github.com/cilium/tetragon/pkg/metrics/opcodemetrics"
	pfmetrics "github.com/cilium/tetragon/pkg/metrics/policyfilter"
	"github.com/cilium/tetragon/pkg/metrics/processexecmetrics"
	"github.com/cilium/tetragon/pkg/metrics/ringbufmetrics"
	"github.com/cilium/tetragon/pkg/metrics/syscallmetrics"
	"github.com/cilium/tetragon/pkg/metrics/watchermetrics"
	"github.com/cilium/tetragon/pkg/observer"
	"github.com/cilium/tetragon/pkg/podhooks"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/workqueue"
)

var (
	metricsWithPod     []*prometheus.MetricVec
	metricsWithPodOnce sync.Once
	podQueue           workqueue.DelayingInterface
	podQueueOnce       sync.Once
	deleteDelay        = 1 * time.Minute
)

// RegisterPodDeleteHandler registers handler for deleting metrics associated
// with deleted pods. Without it, Tetragon kept exposing stale metrics for
// deleted pods. This was causing continuous increase in memory usage in
// Tetragon agent as well as in the metrics scraper.
func RegisterPodDeleteHandler() {
	logger.GetLogger().Info("Registering pod delete handler for metrics")
	podhooks.RegisterCallbacksAtInit(podhooks.Callbacks{
		PodCallbacks: func(podInformer cache.SharedIndexInformer) {
			podInformer.AddEventHandler(
				cache.ResourceEventHandlerFuncs{
					DeleteFunc: func(obj interface{}) {
						var pod *corev1.Pod
						switch concreteObj := obj.(type) {
						case *corev1.Pod:
							pod = concreteObj
						case cache.DeletedFinalStateUnknown:
							// Handle the case when the watcher missed the deletion event
							// (e.g. due to a lost apiserver connection).
							deletedObj, ok := concreteObj.Obj.(*corev1.Pod)
							if !ok {
								return
							}
							pod = deletedObj
						default:
							return
						}
						queue := GetPodQueue()
						queue.AddAfter(pod, deleteDelay)
					},
				},
			)
		},
	})
}

func GetPodQueue() workqueue.DelayingInterface {
	podQueueOnce.Do(func() {
		podQueue = workqueue.NewDelayingQueue()
	})
	return podQueue
}

// ListMetricsWithPod returns the global list of all metrics that have "pod"
// and "namespace" labels, initializing it if needed.
func ListMetricsWithPod() []*prometheus.MetricVec {
	metricsWithPodOnce.Do(func() {
		metricsWithPod = append(metricsWithPod, eventmetrics.ListMetricsWithPod()...)
		metricsWithPod = append(metricsWithPod, syscallmetrics.ListMetricsWithPod()...)
	})
	return metricsWithPod
}

func DeleteMetricsForPod(pod *corev1.Pod) {
	for _, metric := range ListMetricsWithPod() {
		metric.DeletePartialMatch(prometheus.Labels{
			"pod":       pod.Name,
			"namespace": pod.Namespace,
		})
	}
}

func InitAllMetrics(registry *prometheus.Registry) {
	errormetrics.InitMetrics(registry)
	eventcachemetrics.InitMetrics(registry)
	eventmetrics.InitMetrics(registry)
	kprobemetrics.InitMetrics(registry)
	mapmetrics.InitMetrics(registry)
	opcodemetrics.InitMetrics(registry)
	pfmetrics.InitMetrics(registry)
	processexecmetrics.InitMetrics(registry)
	ringbufmetrics.InitMetrics(registry)
	syscallmetrics.InitMetrics(registry)
	watchermetrics.InitMetrics(registry)
	observer.InitMetrics(registry)
	tracing.InitMetrics(registry)
}

func StartPodDeleteHandler() {
	queue := GetPodQueue()
	for {
		pod, quit := queue.Get()
		if quit {
			return
		}
		DeleteMetricsForPod(pod.(*corev1.Pod))
	}
}

func EnableMetrics(address string) {
	reg := prometheus.NewRegistry()
	InitAllMetrics(reg)
	logger.GetLogger().WithField("addr", address).Info("Starting metrics server")
	http.Handle("/metrics", promhttp.HandlerFor(reg, promhttp.HandlerOpts{Registry: reg}))
	http.ListenAndServe(address, nil)
}
