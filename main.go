package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net"
	"net/netip"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/mdlayher/netlink"

	"github.com/ti-mo/conntrack"
	"github.com/ti-mo/netfilter"

	"github.com/lrita/cmap"

	"github.com/hashicorp/golang-lru/v2/expirable"

	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/util/homedir"
)

type Flow struct {
	Timestamp time.Time `json:"timestamp"`

	Src       netip.Addr        `json:"src"`
	SrcPort   uint16            `json:"srcPort"`
	SrcNs     string            `json:"srcNs,omitempty"`
	SrcName   string            `json:"srcName,omitempty"`
	SrcLabels map[string]string `json:"srcLbls,omitempty"`

	Dest      netip.Addr        `json:"dst"`
	DstPort   uint16            `json:"dstPort"`
	DstNs     string            `json:"dstNs,omitempty"`
	DstName   string            `json:"dstName,omitempty"`
	DstLabels map[string]string `json:"dstLbls,omitempty"`

	Proto string `json:"proto"`

	New    bool `json:"new"`
	Update bool `json:"update"`

	Cnt uint32 `json:"cnt"`
}

// Concurrent Map: https://github.com/lrita/cmap
var podLUT cmap.Map[string, *v1.Pod]
var flows cmap.Map[string, *Flow]

var dnsLUT = expirable.NewLRU[string, string](512, nil, time.Second*2)

var podInformerMutex sync.Mutex

func start_conntrack() {
	// Open a Conntrack connection.
	c, err := conntrack.Dial(nil)
	if err != nil {
		log.Fatal(err)
	}

	// Make a buffered channel to receive event updates on.
	evCh := make(chan conntrack.Event, 2048)

	// Listen for all Conntrack and Conntrack-Expect events with 4 decoder goroutines.
	// All errors caught in the decoders are passed on channel errCh.
	errCh, err := c.Listen(evCh, 4, append(netfilter.GroupsCT, netfilter.GroupsCTExp...))
	if err != nil {
		log.Fatal(err)
	}

	// Listen to Conntrack events from all network namespaces on the system.
	err = c.SetOption(netlink.ListenAllNSID, true)
	if err != nil {
		log.Fatal(err)
	}

	// Start a goroutine to print all incoming messages on the event channel.
	go func() {
		for {
			evt := <-evCh

			if evt.Flow.TupleOrig.IP.SourceAddress.IsLoopback() {
				// skip logging of loopback connections
				continue
			}

			// lookup IP in PodLUT
			srcPod, srcIsPod := rDNSPod(evt.Flow.TupleOrig.IP.SourceAddress.String())
			dstPod, dstIsPod := rDNSPod(evt.Flow.TupleOrig.IP.DestinationAddress.String())

			// lookup IP via reverse DNS
			var srcName string
			if !srcIsPod {
				srcName, _ = rDNSHost(evt.Flow.TupleOrig.IP.SourceAddress.String())
			} else {
				srcName = srcPod.Name
			}

			var dstName string
			if !dstIsPod {
				dstName, _ = rDNSHost(evt.Flow.TupleOrig.IP.DestinationAddress.String())
			} else {
				dstName = fmt.Sprintf("%s:%s", dstPod.Namespace, dstPod.Name)
			}

			flowKey := evt.Flow.TupleOrig.String()
			var protoStr string
			if evt.Flow.TupleOrig.Proto.Protocol == 0x6 {
				protoStr = "TCP"
			} else {
				protoStr = "UDP"
			}

			// timeNano := evt.Flow.Timestamp.Start.UnixNano()
			// timeSecs := evt.Flow.Timestamp.Start.Unix()

			flow, existingFlow := flows.LoadOrStore(flowKey, &Flow{
				Timestamp: time.Now(),

				Src:     evt.Flow.TupleOrig.IP.SourceAddress,
				SrcPort: evt.Flow.TupleOrig.Proto.SourcePort,

				Dest:    evt.Flow.TupleOrig.IP.DestinationAddress,
				DstPort: evt.Flow.TupleOrig.Proto.DestinationPort,

				Proto: protoStr,

				Cnt: 0,
			})

			flowEvtPublish := true
			if evt.Type == conntrack.EventNew {
				flow.New = true
			} else if evt.Type == conntrack.EventUpdate {
				flowEvtPublish = !flow.Update
				flow.Update = true
			}

			if srcIsPod {
				flow.SrcNs = srcPod.Namespace
				flow.SrcLabels = srcPod.Labels
			}
			flow.SrcName = srcName

			if dstIsPod {
				flow.DstNs = dstPod.Namespace
				flow.DstLabels = dstPod.Labels
			}
			flow.DstName = dstName

			flow.Cnt++

			// create Flow record, print as collapsed JSON to stdout
			if flowEvtPublish {
				jsonBytes, _ := json.Marshal(flow)
				fmt.Printf("%s (%t) - %s\n", flowKey, existingFlow, string(jsonBytes))
			}
		}
	}()

	// Stop the program as soon as an error is caught in a decoder goroutine.
	log.Print(<-errCh)
}

func rDNSPod(ip string) (*v1.Pod, bool) {
	// PodLUT
	pod, ok := podLUT.Load(ip)
	return pod, ok
}

func rDNSHost(ip string) (string, bool) {
	cacheName, cacheHit := dnsLUT.Get(ip)
	if cacheHit {
		return cacheName, true
	}

	names, err := net.LookupAddr(ip)
	if err != nil {
		return "", false
	} else {
		cacheName = strings.TrimSuffix(names[0], ".")
		dnsLUT.Add(ip, cacheName)
		return cacheName, true
	}
}

func start_pod_informer() {
	// https://www.cncf.io/blog/2019/10/15/extend-kubernetes-via-a-shared-informer/
	// watch for pod add, update and delete events
	// maintain a LUT for pod IP to namespace, name and labels
	// Create a Kubernetes clientset
	// creates the in-cluster config
	var kubeconfig *string
	if home := homedir.HomeDir(); home != "" {
		kubeconfig = flag.String("kubeconfig", filepath.Join(home, ".kube", "config"), "(optional) absolute path to the kubeconfig file")
	} else {
		kubeconfig = flag.String("kubeconfig", "", "absolute path to the kubeconfig file")
	}
	flag.Parse()

	config, err := clientcmd.BuildConfigFromFlags("", *kubeconfig)
	if err != nil {
		panic(err)
	}

	// config, err := rest.InClusterConfig()
	if err != nil {
		panic(err.Error())
	}

	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		log.Fatal(err)
	}

	// Define the watch options
	options := metav1.ListOptions{
		// Set the resource type to watch (e.g., "pods", "deployments")
		Watch: true,
	}

	// Initiate the watch request
	watcher, err := clientset.CoreV1().Pods("").Watch(context.TODO(), options)
	if err != nil {
		log.Fatal(err)
	}

	// Start watching for events
	ch := watcher.ResultChan()
	podInformerMutex.Unlock()
	for event := range ch {
		pod, ok := event.Object.(*v1.Pod)
		if !ok {
			log.Println("Failed to get Pod object from event")
			continue
		}

		// Process the event based on its type
		switch event.Type {
		case watch.Added:
			fmt.Printf("Pod added: %s (%s)\n", pod.Name, pod.Status.PodIP)
			if pod.Status.PodIP != pod.Status.HostIP {
				podLUT.Store(pod.Status.PodIP, pod)
			}
		case watch.Modified:
			fmt.Printf("Pod modified: %s\n", pod.Name)
			if pod.Status.PodIP != pod.Status.HostIP {
				podLUT.Store(pod.Status.PodIP, pod)
			}
		case watch.Deleted:
			fmt.Printf("Pod deleted: %s (%s)\n", pod.Name, pod.Status.PodIP)
			podLUT.Delete(pod.Status.PodIP)
		case watch.Error:
			log.Printf("Error occurred while watching Pod: %s\n", event.Object)
		}
	}

	// Close the watcher when done
	watcher.Stop()
}

func main() {
	podInformerMutex.Lock()
	go start_pod_informer()
	podInformerMutex.Lock()
	start_conntrack()
}
