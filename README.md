# Conntrack event logger for Kubernetes

Capture conntrack events, enrich Pod IPs with K8s metadata for that Pod IP and write to a json log format

## Local testing with KinD

```bash
# https://kind.sigs.k8s.io/
kind create cluster

go build && docker cp ./m kind-control-plane:/m && docker exec -ti kind-control-plane ./m --kubeconfig /etc/kubernetes/admin.conf
```
