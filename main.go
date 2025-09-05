package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"time"

	coordinationv1 "k8s.io/api/coordination/v1"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/util/homedir"
)

type LeaseInfo struct {
	Holder                     string `json:"holder_identity"`
	AcquireTime                int64  `json:"acquire_time"`
	ActualHolderChangedCounter int8   `json:"actual_holder_changed_counter"`
}

var (
	actualHolder               string = ""
	acquireTime                int64
	actualHolderChangedCounter uint8 = 0

	sendRequestPort  string
	sendRequestPath  string
	sendRequestHttps bool

	endpoint string

	kubeVipLeaseName   string
	namespace          string
	daemonSetName      string
	serviceHost        string
	clientSet          *kubernetes.Clientset
	downloadFirstLease bool = false
)

func main() {

	endpointEnv := getEnv("ENDPOINT", "0.0.0.0:8080")
	sendRequestPortEnv := getEnv("SEND_REQUEST_PORT", "8080")
	sendRequestPathEnv := getEnv("SEND_REQUEST_PATH", "/leader")
	sendRequestHttpsEnv, httpsBoolErr := strconv.ParseBool(getEnv("SEND_REQUEST_HTTPS", "false"))
	if httpsBoolErr != nil {
		fmt.Println("Error:", httpsBoolErr)
		return
	}

	kubeVipLeaseNameEnv := getEnv("KUBE_VIP_LEASE_NAME", "plndr-cp-lock")
	namespaceEnv := getEnv("NAMESPACE", "kube-system")
	daemonSetNameEnv := getEnv("DAEMONSET_NAME", "kube-vip-cp-change-lease")
	serviceHostEnv := getEnv("SERVICE_HOST", "")

	flag.StringVar(&endpoint, "endpoint", endpointEnv, "HTTP server endpoint for health and return info with actual lease name")
	flag.StringVar(&sendRequestPort, "daemonset_port", sendRequestPortEnv, "When holder change, controller will find daemonset on that node and send POST request to that port")
	flag.StringVar(&sendRequestPath, "daemonset_path", sendRequestPathEnv, "When holder change, controller will find daemonset on that node and send POST request to that path")

	flag.StringVar(&kubeVipLeaseName, "lease", kubeVipLeaseNameEnv, "Lease name for kube vip")
	flag.StringVar(&namespace, "namespace", namespaceEnv, "Namespace where DaemonSet are installed")
	flag.StringVar(&daemonSetName, "daemonset_name", daemonSetNameEnv, "Deamonset name for find to send webhook when lease change")
	flag.StringVar(&serviceHost, "service_host", serviceHostEnv, "If you not want to search Daemonset and simple send request to service. Set service host")

	// server advertise-route-primary
	help := flag.Bool("h", false, "Show help")
	flag.BoolVar(&sendRequestHttps, "https", sendRequestHttpsEnv, "Send request to daemonset with https")

	// Parse CLI flags
	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage: %s [options] [args]\n", os.Args[0])
		fmt.Fprintln(os.Stderr, "Options:")
		flag.PrintDefaults() // automatycznie wypisuje wszystkie zdefiniowane flagi
	}

	flag.Parse()

	if *help {
		flag.Usage()
		os.Exit(0)
	}

	if daemonSetName == "" && serviceHost == "" {
		panic("You have to set one of: daemonset_name and service_name")
	}

	// Get kubernetes client
	var err error
	clientSet, err = getK8sClient()
	if err != nil {
		fmt.Printf("Error with k8s", err)
	}

	// On start check actual lease
	go checkActualLease()

	// Start http server
	go func() {
		http.HandleFunc("/healthz", healthHandler)
		http.HandleFunc("/info", actualLeaseHandler)
		fmt.Println("HTTP server listening on", endpoint)
		if err := http.ListenAndServe(endpoint, nil); err != nil {
			panic(err)
		}
	}()

	// Start watch lease
	watchLease()

}

func isOwnedByDaemonSet(pod *v1.Pod, dsName string) bool {
	for _, owner := range pod.OwnerReferences {
		if owner.Kind == "DaemonSet" && owner.Name == dsName {
			return true
		}
	}
	return false
}

func getPodIpByDaemonSet(nodeName *string) (bool, string) {
	// List pods in the namespace
	pods, err := clientSet.CoreV1().Pods(namespace).List(context.TODO(), metav1.ListOptions{})
	if err != nil {
		panic(err.Error())
	}

	fmt.Printf("Looking for DaemonSet '%s' on node '%s'\n", daemonSetName, *nodeName)
	for _, pod := range pods.Items {
		// Check if pod is on the desired node
		if pod.Spec.NodeName != *nodeName {
			continue
		}

		// Check if pod belongs to the desired DaemonSet
		if isOwnedByDaemonSet(&pod, daemonSetName) {
			return true, pod.Status.PodIP
		}
	}

	return false, ""
}

func getPodIpByDaemonSetForever(nodeName *string) string {

	if *nodeName == "" {
		return ""
	}

	actualHolderChangedCounterCopy := actualHolderChangedCounter

	for {

		if actualHolderChangedCounterCopy != actualHolderChangedCounter {
			fmt.Println("ActualHolder value changed during find pod ip.")
			return ""
		}

		found, podIp := getPodIpByDaemonSet(nodeName)
		if found {
			return podIp
		}

		fmt.Println("Waiting 2 seconds before next retry for get daemonset info...")
		time.Sleep(5 * time.Second) // wait before retry
	}

}

func getKubeConfig() (*rest.Config, error) {
	// Try in-cluster first
	config, err := rest.InClusterConfig()
	if err == nil {
		return config, nil
	}

	// Fallback: use kubeconfig file
	var kubeconfig string
	if home := homedir.HomeDir(); home != "" {
		kubeconfig = filepath.Join(home, ".kube", "config")
	}

	// Allow overriding path via KUBECONFIG env var
	if env := os.Getenv("KUBECONFIG"); env != "" {
		kubeconfig = env
	}

	fmt.Printf("Using kubeconfig: %s\n", kubeconfig)
	return clientcmd.BuildConfigFromFlags("", kubeconfig)
}

func getK8sClient() (*kubernetes.Clientset, error) {
	// In-cluster config (works when running inside k8s pod)
	config, err := getKubeConfig()
	if err != nil {
		log.Fatal("failed to load in-cluster config: %w", err)
	}

	c, err := kubernetes.NewForConfig(config)
	if err != nil {
		log.Fatal("failed to create k8s client: %w", err)
	}

	return c, nil
}

func getCurrentLeaseHolder() (*coordinationv1.Lease, error) {
	fmt.Println("Try to get actual Lease holder for:", kubeVipLeaseName)

	lease, err := clientSet.CoordinationV1().Leases(namespace).Get(
		context.TODO(),
		kubeVipLeaseName,
		metav1.GetOptions{},
	)

	if err != nil {
		return nil, fmt.Errorf("failed to get kube-vip lease: %w", err)
	}

	if lease.Spec.HolderIdentity == nil {
		return nil, fmt.Errorf("Lease do not has holderIdentity: %w", err)
	}

	return lease, nil
}

func sendLeaseRequest(destinationUrlOrIP string, LeaseName string) error {
	fmt.Println("Start sending request")
	data := map[string]string{
		"holder_identity": LeaseName,
	}

	jsonData, err := json.Marshal(data)
	if err != nil {
		return fmt.Errorf("failed to marshal JSON: %w", err)
	}

	scheme := "http"
	if sendRequestHttps {
		scheme = "https"
	}

	url := scheme + "://" + destinationUrlOrIP + ":" + sendRequestPort + sendRequestPath
	fmt.Printf("Send request to: %s\n", url)
	actualHolderChangedCounterCopy := actualHolderChangedCounter

	var retryCounter = 0
	for {
		// If actual holder changed during send request break
		if actualHolderChangedCounterCopy != actualHolderChangedCounter {
			return fmt.Errorf("ActualHolder changed during send %d attempts request . Stop send request", retryCounter)
		}

		resp, err := http.Post(url, "application/json", bytes.NewBuffer(jsonData))
		if err != nil {
			fmt.Printf("Attempt %d: request error: %v\n", retryCounter, err)
		} else {
			body, _ := io.ReadAll(resp.Body)
			resp.Body.Close()

			if resp.StatusCode >= 200 && resp.StatusCode < 300 {
				fmt.Println("Request successful:", string(body))
				return nil
			}

			fmt.Printf("Attempt %d: server returned status %d: %s\n", retryCounter, resp.StatusCode, string(body))

			if resp.StatusCode >= 400 && resp.StatusCode < 500 {
				return nil
			}
		}

		fmt.Println("Waiting 2 seconds before next retry...")
		time.Sleep(2 * time.Second) // wait before retry
		retryCounter++
	}

}

func healthHandler(w http.ResponseWriter, r *http.Request) {
	if !downloadFirstLease {
		w.WriteHeader(http.StatusInternalServerError)
	} else {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ok"))
	}

}

func actualLeaseHandler(w http.ResponseWriter, r *http.Request) {

	response := LeaseInfo{
		Holder:                     actualHolder,
		AcquireTime:                acquireTime,
		ActualHolderChangedCounter: int8(actualHolderChangedCounter),
	}

	jsonData, err := json.Marshal(response)
	if err != nil {
		http.Error(w, "Error encoding JSON", http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusOK)
	w.Header().Set("Content-Type", "application/json")
	w.Write(jsonData)

}

func checkActualLease() {
	lease, err := getCurrentLeaseHolder()
	print(err)
	if err != nil {
		log.Println("Cant get current Lease holder", err)
		return
	}
	downloadFirstLease = true
	onLeaseHandlerChanged(lease)
}

func getEnv(key, defaultVal string) string {
	if val, exists := os.LookupEnv(key); exists {
		return val
	}
	return defaultVal
}

func onLeaseHandlerChanged(lease *coordinationv1.Lease) error {
	downloadFirstLease = true
	actualHolder = holderAsString(lease)
	acquireTime = lease.Spec.AcquireTime.Unix()
	actualHolderChangedCounter++

	// Get destination to send
	var destinationServiceOrIp string
	if serviceHost != "" {
		destinationServiceOrIp = serviceHost
		fmt.Printf("Sending requetst to service: %s", serviceHost)
	} else {
		destinationServiceOrIp = getPodIpByDaemonSetForever(lease.Spec.HolderIdentity)
		if destinationServiceOrIp == "" {
			return fmt.Errorf("Cant find pod ip for daemonset %s", daemonSetName)
		}
		fmt.Printf("For daemonSet %s, found pod ip: %s\n", daemonSetName, destinationServiceOrIp)

	}

	go sendLeaseRequest(destinationServiceOrIp, *lease.Spec.HolderIdentity)
	return nil
}

func watchLease() {

	// Definiujemy ListerWatcher dla konkretnego Lease
	lw := &cache.ListWatch{
		ListFunc: func(options metav1.ListOptions) (runtime.Object, error) {
			options.FieldSelector = fields.OneTermEqualSelector("metadata.name", "plndr-cp-lock").String()
			return clientSet.CoordinationV1().Leases(namespace).List(context.TODO(), options)
		},
		WatchFunc: func(options metav1.ListOptions) (watch.Interface, error) {
			options.FieldSelector = fields.OneTermEqualSelector("metadata.name", "plndr-cp-lock").String()
			return clientSet.CoordinationV1().Leases(namespace).Watch(context.TODO(), options)
		},
	}

	// Tworzymy informer bez handlerów
	informer := cache.NewSharedInformer(
		lw,
		&coordinationv1.Lease{},
		30*time.Second,
	)

	informer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			lease := obj.(*coordinationv1.Lease)
			fmt.Printf("[ADDED] Lease %s holder=%v\n", lease.Name, *lease.Spec.HolderIdentity)
			if lease.Name == kubeVipLeaseName {
				onLeaseHandlerChanged(lease)
			}
		},
		UpdateFunc: func(oldObj, newObj interface{}) {
			oldLease := oldObj.(*coordinationv1.Lease)
			newLease := newObj.(*coordinationv1.Lease)
			if kubeVipLeaseName == newLease.Name {

				newHolderIdentity := holderAsString(newLease)
				oldHolderIdentity := holderAsString(oldLease)

				// porównanie: HolderIdentity albo RenewTime
				if oldHolderIdentity != newHolderIdentity ||
					!renewTimeEqual(oldLease.Spec.AcquireTime, newLease.Spec.AcquireTime) {
					fmt.Printf("[CHANGED] Lease %s holder=%s AcquireTime=%v\n",
						newLease.Name, newHolderIdentity, newLease.Spec.AcquireTime)
					onLeaseHandlerChanged(newLease)
				}
			}
		},
		DeleteFunc: func(obj interface{}) {
			lease := obj.(*coordinationv1.Lease)
			fmt.Printf("[DELETED] Lease %s\n", lease.Name)
		},
	})

	stop := make(chan struct{})
	defer close(stop)

	fmt.Println("Starting...")
	informer.Run(stop)

}

func renewTimeEqual(t1, t2 *metav1.MicroTime) bool {
	if t1 == nil && t2 == nil {
		return true
	}
	if t1 == nil || t2 == nil {
		return false
	}
	return t1.Time.Equal(t2.Time)
}

func holderAsString(lease *coordinationv1.Lease) string {
	if lease.Spec.HolderIdentity != nil {
		return *lease.Spec.HolderIdentity
	}
	return "<nil>"
}
