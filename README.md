# Kube-vip controller
This is a kube-vip controller which will listen for change new lease of kube-vip.
When received event from api-server that lease has changed will send http request to a daemonset.
This controller can be use for all cloud networks.

Example: 
- In hetzner hcloud during change private network ip this cloud require send request for set alias ip in hcloud api. 
- Headscale during change advertise route require change it in headscale server.

This is a only kube-vip-controller. Daemonset you have to write you own. Use this as strategy for end notification to your cloud, when you want to detect change lease of kube-vip.

![alt text](image.jpg)

# Features
- Controller listen for change Leader of kube-vip (Lease).
- If lease has changed, try to find daemonset for new lease (node).
- If not find daemonset on that node will try every 5s to find daemonset until lease change again.
- If daemonset http server do not return code 200 will try until lease change every 2s.

# Parameters
```
Options:
  -daemonset_name string
        Deamonset name for find to send webhook when lease change, default: kube-vip-cp-change-lease (default "kube-vip-cp-change-lease")
  -daemonset_path string
        When holder change, controller will find daemonset on that node and send POST request to that path (default "/leader")
  -daemonset_port string
        When holder change, controller will find daemonset on that node and send POST request to that port (default "8080")
  -endpoint string
        HTTP server endpoint (default "0.0.0.0:8080")
  -https
        Send request to daemonset with https
  -h    Show help
  -lease string
        Lease name for kube vip (default "plndr-cp-lock")
  -namespace string
        Namespace where DaemonSet are installed (default "kube-system")
```

# Install

```sh
docker run -it --rm gawsoftpl/kube-vip-controller
```

# Development
Use `mirrord` for inject local dev code to cluster.

```sh
make build
```

or build and load to cluster
```sh
make docker
kind load docker-image kube-vip-controller --name kube-vip-webhook
```

## Create test cluster
```sh
# Create cluster kind
make cluster
INTERFACE=`docker network inspect kind -f '{{ range $i, $a := .IPAM.Config }}{{ println .Subnet }}{{ end }}'`

# Install kube-vip
alias kube-vip="docker run --network host --rm ghcr.io/kube-vip/kube-vip:v1.0.0"
kubectl apply -f https://kube-vip.io/manifests/rbac.yaml 
kube-vip manifest daemonset \
    --address 172.18.0.100 \
    --services \
    --controlplane \
    --enableLoadBalancer \
    --taint \
    --leaderElection \
    --inCluster \
    --arp \
    --interface eth0 | kubectl apply -f -

# Create k8s manifest
make k8s-manifest

```
