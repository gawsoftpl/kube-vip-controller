cat <<EOF > /tmp/cluster.yaml
kind: Cluster
apiVersion: kind.x-k8s.io/v1alpha4
nodes:
  - role: control-plane
  - role: control-plane
  - role: control-plane
EOF

kind create cluster --config /tmp/cluster.yaml --name kube-vip-webhook