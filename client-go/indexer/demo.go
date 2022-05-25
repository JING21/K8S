package main

import (
	"fmt"
	"strings"

	"k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/cache"
)

func  UsersIndexFunc(obj interface{}) ([]string, error)  {
	pod := obj.(*v1.Pod)
	userString := pod.Annotations["users"]


	return strings.Split(userString, ","), nil
}


func main(){
	index := cache.NewIndexer(cache.MetaNamespaceKeyFunc,cache.Indexers{"byUser":UsersIndexFunc})


	pod1 := &v1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "one", Annotation: map[string]string{"users": "ernie,bert"}}}

	pod2 := &v1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "two", Annotation: map[string]string{"users": "bert,oscar"}}}

	pod3 := &v1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "three", Annotation: map[string]string{"users": "ernie, telsa"}}}


	index.Add(pod1)
	index.Add(pod2)
	index.Add(pod3)

	erniePods, err := index.ByIndex("byUser", "ernie")
	if err != nil{
		panic(err)
	}
	
	for _, v := range erniePods{

		fmt.Println(v.(*v1.Pod).Name)
	}

}