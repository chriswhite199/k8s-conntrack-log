# Conntrack event logger for Kubernetes

Capture conntrack events, enrich Pod IPs with K8s metadata for that Pod IP and write to a json log format

## Local testing with KinD

```bash
# https://kind.sigs.k8s.io/
kind create cluster

go build && docker cp ./m kind-control-plane:/m && docker exec -ti kind-control-plane ./m --kubeconfig /etc/kubernetes/admin.conf
```

### Sample output:
```
Pod added: coredns-7c65d6cfc9-qnlcd (10.244.0.2)
Pod added: coredns-7c65d6cfc9-sq2br (10.244.0.3)
Pod added: etcd-kind-control-plane (172.18.0.2)
Pod added: kindnet-hpfpz (172.18.0.2)
Pod added: kube-apiserver-kind-control-plane (172.18.0.2)
Pod added: kube-controller-manager-kind-control-plane (172.18.0.2)
Pod added: kube-proxy-mnbcb (172.18.0.2)
Pod added: kube-scheduler-kind-control-plane (172.18.0.2)
Pod added: local-path-provisioner-57c5987fd4-5dq5c (10.244.0.4)


# Host traffic
<tcp, Src: 172.18.0.2:37124, Dst: 172.18.0.2:6443> (false) - {"timestamp":"2024-11-25T18:46:18.179965998Z","src":"172.18.0.2","srcPort":37124,"srcName":"kind-control-plane","dst":"172.18.0.2","dstPort":6443,"dstName":"kind-control-plane","proto":"TCP","new":true,"update":false,"cnt":1}
<tcp, Src: 172.18.0.2:37124, Dst: 172.18.0.2:6443> (true) - {"timestamp":"2024-11-25T18:46:18.179965998Z","src":"172.18.0.2","srcPort":37124,"srcName":"kind-control-plane","dst":"172.18.0.2","dstPort":6443,"dstName":"kind-control-plane","proto":"TCP","new":true,"update":true,"cnt":2}

# Pod traffic with namespaces + labels
<tcp, Src: 10.244.0.1:53388, Dst: 10.244.0.3:8181> (false) - {"timestamp":"2024-11-25T18:47:22.852716307Z","src":"10.244.0.1","srcPort":53388,"dst":"10.244.0.3","dstPort":8181,"dstNs":"kube-system","dstName":"kube-system:coredns-7c65d6cfc9-sq2br","dstLbls":{"k8s-app":"kube-dns","pod-template-hash":"7c65d6cfc9"},"proto":"TCP","new":false,"update":true,"cnt":1}
<tcp, Src: 10.244.0.1:53388, Dst: 10.244.0.3:8181> (true) - {"timestamp":"2024-11-25T18:47:22.852716307Z","src":"10.244.0.1","srcPort":53388,"dst":"10.244.0.3","dstPort":8181,"dstNs":"kube-system","dstName":"kube-system:coredns-7c65d6cfc9-sq2br","dstLbls":{"k8s-app":"kube-dns","pod-template-hash":"7c65d6cfc9"},"proto":"TCP","new":true,"update":true,"cnt":2}
```

## Areas for improvement

* Attribute dst pod IPs to service / endpoints
    ```
    root@kind-control-plane:/# kubectl get ep -A 
    NAMESPACE     NAME         ENDPOINTS                                               AGE
    default       kubernetes   172.18.0.2:6443                                         11d
    kube-system   kube-dns     10.244.0.2:53,10.244.0.3:53,10.244.0.2:53 + 3 more...   11d
    ```
* Buffer and aggregate events to reduce output volume
   * Single event for aggregated New + Update event
   * Aggregate changing source ports with the same src IP, dst IP + port into a single record
* Filtering by CIDR
* Write json events to file, file rollover etc
