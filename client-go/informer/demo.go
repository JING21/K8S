package main

import (
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/clientcmd"
	"log"
	"time"
)


func main(){
	 config, err := clientcmd.BuildConfigFromFlags("","/Users/jing/Desktop/config")
	 if err != nil{
	 	panic(err)
	 }

	clientSet, err := kubernetes.NewForConfig(config)
	if err !=nil{
		panic(err)
	}

	stopChan := make(chan struct{})
	defer close(stopChan)

	sharedInformers := informers.NewSharedInformerFactory(clientSet, time.Minute)

	podInformer := sharedInformers.Core().V1().Pods().Informer()

	podInformer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			addObj := obj.(v1.Object)
			log.Printf("New pod Added tp Store %s", addObj.GetName())
		},
	})

	podInformer.Run(stopChan)
}
