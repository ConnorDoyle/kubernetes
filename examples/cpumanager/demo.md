1. Start Kubelet :
   `sudo PATH=$PATH KUBELET_FLAGS="--cpu-manager-policy=static" ./hack/local-up-cluster.sh`
1. TODO: Port [previous demo skeleton](https://gist.github.com/ConnorDoyle/ddfa508473de5c77a0fc931b13f1ff49)
1. TODO: Add examples in every QoS class with comments explaining their
         classifiction and the associated CPU policy semantics. Add
         examples of containers in Guaranteed pods that do not receive
         exclusive CPUs under the static policy (i.e. no CPU request,
         non-integer CPU request)
