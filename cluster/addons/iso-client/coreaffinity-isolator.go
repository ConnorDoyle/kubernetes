package main

import (
	"flag"
	"os"
	"os/signal"
	"sync"
	"syscall"

	"github.com/golang/glog"
	// For newer version of k8s use this package
	//	"k8s.io/apimachinery/pkg/util/uuid"

	aff "k8s.io/kubernetes/cluster/addons/iso-client/coreaffinity"
)

const (
	// kubelet eventDispatcher address
	eventDispatcherAddress = "localhost:5433"
	// iso-client own address
	isolatorLocalAddress = "localhost:5444"
	// name of isolator
	name = "iso"
)

// TODO: split it to smaller functions
func main() {
	flag.Parse()
	glog.Info("Starting ...")
	var wg sync.WaitGroup
	// Starting isolatorServer
	server := aff.NewIsolator(name, isolatorLocalAddress)
	err := server.RegisterIsolator()
	if err != nil {
		glog.Fatalf("Cannot register isolator: %v", err)
		os.Exit(1)
	}
	wg.Add(1)
	go server.Serve(wg)

	// Sending address of local isolatorServer
	client, err := aff.NewEventDispatcherClient(name, eventDispatcherAddress, isolatorLocalAddress)
	if err != nil {
		glog.Fatalf("Cannot create eventDispatcherClient: %v", err)
		os.Exit(1)
	}

	reply, err := client.Register()
	if err != nil {
		glog.Fatalf("Failed to register isolator: %v . Reply: %v", err, reply)
		os.Exit(1)
	}
	glog.Infof("Registering isolator. Reply: %v", reply)

	// Handling SigTerm
	c := make(chan os.Signal, 2)
	signal.Notify(c, os.Interrupt, syscall.SIGTERM)
	go aff.HandleSIGTERM(c, client)

	wg.Wait()
}
