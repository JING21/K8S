# Client-GO源码解析

[toc]

## Informer机制

Kubernetes的其他组件通过informer机制与Kubernetes API Server进行交互通信，保证了消息的实时性，可靠性，顺序性。Informer机制架构如图所示：

![Informer](https://github.com/JING21/K8S/raw/main/client-go/Informer.jpg)

### Reflector组件

Reflector组件用于监控（List and Watch）指定的Kubernetes资源，当资源发生变化时，触发相对应的的事件，比如说ADD(添加资源)，Update（更新资源），Deleted（删除事件），并将其资源存储在本地缓存DeltaFIFO。

其中reflector的结构体定义如下所示：

name表示reflector的唯一标识（通过file:line），expectedTypeName, expectedType, expectedGVK(确认资源类型)，store（存储interface，reflector中的具体实现为DeltaQueue），ListWatcher （用于存储ListWatcher的资源），backoffManager，initConnBackoffManager（用于作用于失败重试，当上游apiserver not healthy时，减轻对apiserver的访问，控制流量），resyncPeriod（informer使用者重新同步的周期），ShouldResync,clock（判断是否满足可以重新同步的条件，paginatedResult（是否强制进行分页List），lastSyncResourceVersion（最后同步的资源版本号，watch只会监听大于此值的资源），isLastSyncResourceVersionUnavailable（最后同步的资源版本号是否可用），lastSyncResourceVersionMutex（最后同步的资源版本号的控制锁），WatchListPageSize（ListWatch分页大小），watchErrorHandler（watch失败回调处理handler）



k8s.io/client-go/tools/cache/reflector.go

```go
// Reflector watches a specified resource and causes all changes to be reflected in the given store.
type Reflector struct {
	// name identifies this reflector. By default it will be a file:line if possible.
	name string

	// The name of the type we expect to place in the store. The name
	// will be the stringification of expectedGVK if provided, and the
	// stringification of expectedType otherwise. It is for display
	// only, and should not be used for parsing or comparison.
	expectedTypeName string
	// An example object of the type we expect to place in the store.
	// Only the type needs to be right, except that when that is
	// `unstructured.Unstructured` the object's `"apiVersion"` and
	// `"kind"` must also be right.
	expectedType reflect.Type
	// The GVK of the object we expect to place in the store if unstructured.
	expectedGVK *schema.GroupVersionKind
	// The destination to sync up with the watch source
	store Store
	// listerWatcher is used to perform lists and watches.
	listerWatcher ListerWatcher

	// backoff manages backoff of ListWatch
	backoffManager wait.BackoffManager
	// initConnBackoffManager manages backoff the initial connection with the Watch call of ListAndWatch.
	initConnBackoffManager wait.BackoffManager

	resyncPeriod time.Duration
	// ShouldResync is invoked periodically and whenever it returns `true` the Store's Resync operation is invoked
	ShouldResync func() bool
	// clock allows tests to manipulate time
	clock clock.Clock
	// paginatedResult defines whether pagination should be forced for list calls.
	// It is set based on the result of the initial list call.
	paginatedResult bool
	// lastSyncResourceVersion is the resource version token last
	// observed when doing a sync with the underlying store
	// it is thread safe, but not synchronized with the underlying store
	lastSyncResourceVersion string
	// isLastSyncResourceVersionUnavailable is true if the previous list or watch request with
	// lastSyncResourceVersion failed with an "expired" or "too large resource version" error.
	isLastSyncResourceVersionUnavailable bool
	// lastSyncResourceVersionMutex guards read/write access to lastSyncResourceVersion
	lastSyncResourceVersionMutex sync.RWMutex
	// WatchListPageSize is the requested chunk size of initial and resync watch lists.
	// If unset, for consistent reads (RV="") or reads that opt-into arbitrarily old data
	// (RV="0") it will default to pager.PageSize, for the rest (RV != "" && RV != "0")
	// it will turn off pagination to allow serving them from watch cache.
	// NOTE: It should be used carefully as paginated lists are always served directly from
	// etcd, which is significantly less efficient and may lead to serious performance and
	// scalability problems.
	WatchListPageSize int64
	// Called whenever the ListAndWatch drops the connection with an error.
	watchErrorHandler WatchErrorHandler
}

```



通过NewReflector()创建一个Reflector，同时传入一个ListWatcher对象和指定的expectedType，存储数据的store的DeltaQueue，重新同步的时间resyncPeriod

k8s.io/client-go/tools/cache/reflector.go

```go
func NewReflector(lw ListerWatcher, expectedType interface{}, store Store, resyncPeriod time.Duration) *Reflector {
	return NewNamedReflector(naming.GetNameFromCallsite(internalPackages...), lw, expectedType, store, resyncPeriod)
}

// NewNamedReflector same as NewReflector, but with a specified name for logging
func NewNamedReflector(name string, lw ListerWatcher, expectedType interface{}, store Store, resyncPeriod time.Duration) *Reflector {
	realClock := &clock.RealClock{}
	r := &Reflector{
		name:          name,
		listerWatcher: lw,
		store:         store,
		// We used to make the call every 1sec (1 QPS), the goal here is to achieve ~98% traffic reduction when
		// API server is not healthy. With these parameters, backoff will stop at [30,60) sec interval which is
		// 0.22 QPS. If we don't backoff for 2min, assume API server is healthy and we reset the backoff.
		backoffManager:         wait.NewExponentialBackoffManager(800*time.Millisecond, 30*time.Second, 2*time.Minute, 2.0, 1.0, realClock),
		initConnBackoffManager: wait.NewExponentialBackoffManager(800*time.Millisecond, 30*time.Second, 2*time.Minute, 2.0, 1.0, realClock),
		resyncPeriod:           resyncPeriod,
		clock:                  realClock,
		watchErrorHandler:      WatchErrorHandler(DefaultWatchErrorHandler),
	}
	r.setExpectedType(expectedType)
	return r
}

func (r *Reflector) setExpectedType(expectedType interface{}) {
	r.expectedType = reflect.TypeOf(expectedType)
	if r.expectedType == nil {
		r.expectedTypeName = defaultExpectedTypeName
		return
	}

	r.expectedTypeName = r.expectedType.String()

	if obj, ok := expectedType.(*unstructured.Unstructured); ok {
		// Use gvk to check that watch event objects are of the desired type.
		gvk := obj.GroupVersionKind()
		if gvk.Empty() {
			klog.V(4).Infof("Reflector from %s configured with expectedType of *unstructured.Unstructured with empty GroupVersionKind.", r.name)
			return
		}
		r.expectedGVK = &gvk
		r.expectedTypeName = gvk.String()
	}
}

```

reflector实例对象通过Run函数启动，在stopChannel不结束的情况下，会不停的运行调用reflector实现的listwatch方法去监听APIServer的资源，其中reflector核心关键的代码是ListWatch的实现和watchhandler的实现。

k8s.io/client-go/tools/cache/reflector.go

```go
func (r *Reflector) Run(stopCh <-chan struct{}) {
	klog.V(3).Infof("Starting reflector %s (%s) from %s", r.expectedTypeName, r.resyncPeriod, r.name)
	wait.BackoffUntil(func() {
		if err := r.ListAndWatch(stopCh); err != nil {
			r.watchErrorHandler(r, err)
		}
	}, r.backoffManager, true, stopCh)
	klog.V(3).Infof("Stopping reflector %s (%s) from %s", r.expectedTypeName, r.resyncPeriod, r.name)
}
```

ListAndWatch的具体流程包含以下几个函数，首先调用一个goroutine 去获取list,pager.New()新建pager来达到分chunk收集list，如果ListWatcher的具体实现不支持分chunked，则会获取全部的List信息，其中reflector中的listerWatcher（ListerWatcher类型）实现Lister接口（其中包含了List方法的具体实现是listwatch包中ListWatch结构体的具体实现），同时如果List失败后，会判断resourcesversion是否合理有效，直接进行重试。

paginatedResult返回结果为true以及resourcesVersion为0时，表示watchCache处于disable的状态同时，同时有多个同一已知类型的object对象，此时表明不需要从watch cache list对象了。（这只会发生在初始化init list的时候），设置这个判断逻辑的原因，是因为我们有时候会设置options，将resourcesVersion设置为空" ",表示直接从etcd list对象。

然后meta.ListAccessor()将list结果转换为listMetaInterface,接着使用listMetaInterface.GetResourceVersion来获取资源版本号，紧接着使用meta.ExtractList()将资源数据转换为资源对象列表items，然后使用r.syncWith(items, resourceVersion)将资源对象列表和资源版本号存储至DeltaFIFO中，全量替换本地缓存的对象，最后使用r.setLastSyncResourceVersion(resourceVersion)更新设置最新的版本号。

第二个goroutine调用了r.store.Resync()方法，当r.ShouldResync == nil 或者r.ShouldResync()为true的情况下，会重新同步DeltaFIFO的object。

k8s.io/client-go/tools/cache/reflector.go

```go
// ListAndWatch first lists all items and get the resource version at the moment of call,
// and then use the resource version to watch.
// It returns error if ListAndWatch didn't even try to initialize watch.
func (r *Reflector) ListAndWatch(stopCh <-chan struct{}) error {
	klog.V(3).Infof("Listing and watching %v from %s", r.expectedTypeName, r.name)
	var resourceVersion string

	options := metav1.ListOptions{ResourceVersion: r.relistResourceVersion()}

	if err := func() error {
		initTrace := trace.New("Reflector ListAndWatch", trace.Field{"name", r.name})
		defer initTrace.LogIfLong(10 * time.Second)
		var list runtime.Object
		var paginatedResult bool
		var err error
		listCh := make(chan struct{}, 1)
		panicCh := make(chan interface{}, 1)
		go func() {
			defer func() {
				if r := recover(); r != nil {
					panicCh <- r
				}
			}()
			// Attempt to gather list in chunks, if supported by listerWatcher, if not, the first
			// list request will return the full response.
			pager := pager.New(pager.SimplePageFunc(func(opts metav1.ListOptions) (runtime.Object, error) {
				return r.listerWatcher.List(opts)
			}))
			switch {
			case r.WatchListPageSize != 0:
				pager.PageSize = r.WatchListPageSize
			case r.paginatedResult:
				// We got a paginated result initially. Assume this resource and server honor
				// paging requests (i.e. watch cache is probably disabled) and leave the default
				// pager size set.
			case options.ResourceVersion != "" && options.ResourceVersion != "0":
				// User didn't explicitly request pagination.
				//
				// With ResourceVersion != "", we have a possibility to list from watch cache,
				// but we do that (for ResourceVersion != "0") only if Limit is unset.
				// To avoid thundering herd on etcd (e.g. on master upgrades), we explicitly
				// switch off pagination to force listing from watch cache (if enabled).
				// With the existing semantic of RV (result is at least as fresh as provided RV),
				// this is correct and doesn't lead to going back in time.
				//
				// We also don't turn off pagination for ResourceVersion="0", since watch cache
				// is ignoring Limit in that case anyway, and if watch cache is not enabled
				// we don't introduce regression.
				pager.PageSize = 0
			}

			list, paginatedResult, err = pager.List(context.Background(), options)
			if isExpiredError(err) || isTooLargeResourceVersionError(err) {
				r.setIsLastSyncResourceVersionUnavailable(true)
				// Retry immediately if the resource version used to list is unavailable.
				// The pager already falls back to full list if paginated list calls fail due to an "Expired" error on
				// continuation pages, but the pager might not be enabled, the full list might fail because the
				// resource version it is listing at is expired or the cache may not yet be synced to the provided
				// resource version. So we need to fallback to resourceVersion="" in all to recover and ensure
				// the reflector makes forward progress.
				list, paginatedResult, err = pager.List(context.Background(), metav1.ListOptions{ResourceVersion: r.relistResourceVersion()})
			}
			close(listCh)
		}()
		select {
		case <-stopCh:
			return nil
		case r := <-panicCh:
			panic(r)
		case <-listCh:
		}
		initTrace.Step("Objects listed", trace.Field{"error", err})
		if err != nil {
			klog.Warningf("%s: failed to list %v: %v", r.name, r.expectedTypeName, err)
			return fmt.Errorf("failed to list %v: %v", r.expectedTypeName, err)
		}

		// We check if the list was paginated and if so set the paginatedResult based on that.
		// However, we want to do that only for the initial list (which is the only case
		// when we set ResourceVersion="0"). The reasoning behind it is that later, in some
		// situations we may force listing directly from etcd (by setting ResourceVersion="")
		// which will return paginated result, even if watch cache is enabled. However, in
		// that case, we still want to prefer sending requests to watch cache if possible.
		//
		// Paginated result returned for request with ResourceVersion="0" mean that watch
		// cache is disabled and there are a lot of objects of a given type. In such case,
		// there is no need to prefer listing from watch cache.
		if options.ResourceVersion == "0" && paginatedResult {
			r.paginatedResult = true
		}

		r.setIsLastSyncResourceVersionUnavailable(false) // list was successful
		listMetaInterface, err := meta.ListAccessor(list)
		if err != nil {
			return fmt.Errorf("unable to understand list result %#v: %v", list, err)
		}
		resourceVersion = listMetaInterface.GetResourceVersion()
		initTrace.Step("Resource version extracted")
		items, err := meta.ExtractList(list)
		if err != nil {
			return fmt.Errorf("unable to understand list result %#v (%v)", list, err)
		}
		initTrace.Step("Objects extracted")
		if err := r.syncWith(items, resourceVersion); err != nil {
			return fmt.Errorf("unable to sync list result: %v", err)
		}
		initTrace.Step("SyncWith done")
		r.setLastSyncResourceVersion(resourceVersion)
		initTrace.Step("Resource version updated")
		return nil
	}(); err != nil {
		return err
	}

	resyncerrc := make(chan error, 1)
	cancelCh := make(chan struct{})
	defer close(cancelCh)
	go func() {
		resyncCh, cleanup := r.resyncChan()
		defer func() {
			cleanup() // Call the last one written into cleanup
		}()
		for {
			select {
			case <-resyncCh:
			case <-stopCh:
				return
			case <-cancelCh:
				return
			}
			if r.ShouldResync == nil || r.ShouldResync() {
				klog.V(4).Infof("%s: forcing resync", r.name)
				if err := r.store.Resync(); err != nil {
					resyncerrc <- err
					return
				}
			}
			cleanup()
			resyncCh, cleanup = r.resyncChan()
		}
	}()
}
```

最后Watch操作通过HTTP协议与Kubernetes API Server建立长链接，接收Kubernetes API Server发来的资源变更事件。具体调用函数r.listerWatcher.Watch(options)，实际调用了具体Informer下的Watch函数，比如说pod informer的client.CoreV1().Pods(namespace).Watch，r.WatchHandler()用于处理资源变更事件，将对应的资源更新到本地的DeltaFIFO中，并更新其资源版本号ResourceVersion

k8s.io/client-go/tools/cache/reflector.go

```go

	for {
		// give the stopCh a chance to stop the loop, even in case of continue statements further down on errors
		select {
		case <-stopCh:
			return nil
		default:
		}

		timeoutSeconds := int64(minWatchTimeout.Seconds() * (rand.Float64() + 1.0))
		options = metav1.ListOptions{
			ResourceVersion: resourceVersion,
			// We want to avoid situations of hanging watchers. Stop any wachers that do not
			// receive any events within the timeout window.
			TimeoutSeconds: &timeoutSeconds,
			// To reduce load on kube-apiserver on watch restarts, you may enable watch bookmarks.
			// Reflector doesn't assume bookmarks are returned at all (if the server do not support
			// watch bookmarks, it will ignore this field).
			AllowWatchBookmarks: true,
		}

		// start the clock before sending the request, since some proxies won't flush headers until after the first watch event is sent
		start := r.clock.Now()
		w, err := r.listerWatcher.Watch(options)
		if err != nil {
			// If this is "connection refused" error, it means that most likely apiserver is not responsive.
			// It doesn't make sense to re-list all objects because most likely we will be able to restart
			// watch where we ended.
			// If that's the case begin exponentially backing off and resend watch request.
			// Do the same for "429" errors.
			if utilnet.IsConnectionRefused(err) || apierrors.IsTooManyRequests(err) {
				<-r.initConnBackoffManager.Backoff().C()
				continue
			}
			return err
		}

		if err := r.watchHandler(start, w, &resourceVersion, resyncerrc, stopCh); err != nil {
			if err != errorStopRequested {
				switch {
				case isExpiredError(err):
					// Don't set LastSyncResourceVersionUnavailable - LIST call with ResourceVersion=RV already
					// has a semantic that it returns data at least as fresh as provided RV.
					// So first try to LIST with setting RV to resource version of last observed object.
					klog.V(4).Infof("%s: watch of %v closed with: %v", r.name, r.expectedTypeName, err)
				case apierrors.IsTooManyRequests(err):
					klog.V(2).Infof("%s: watch of %v returned 429 - backing off", r.name, r.expectedTypeName)
					<-r.initConnBackoffManager.Backoff().C()
					continue
				default:
					klog.Warningf("%s: watch of %v ended with: %v", r.name, r.expectedTypeName, err)
				}
			}
			return nil
		}
	}


// watchHandler watches w and keeps *resourceVersion up to date.
func (r *Reflector) watchHandler(start time.Time, w watch.Interface, resourceVersion *string, errc chan error, stopCh <-chan struct{}) error {
	eventCount := 0

	// Stopping the watcher should be idempotent and if we return from this function there's no way
	// we're coming back in with the same watch interface.
	defer w.Stop()

loop:
	for {
		select {
		case <-stopCh:
			return errorStopRequested
		case err := <-errc:
			return err
		case event, ok := <-w.ResultChan():
			if !ok {
				break loop
			}
			if event.Type == watch.Error {
				return apierrors.FromObject(event.Object)
			}
			if r.expectedType != nil {
				if e, a := r.expectedType, reflect.TypeOf(event.Object); e != a {
					utilruntime.HandleError(fmt.Errorf("%s: expected type %v, but watch event object had type %v", r.name, e, a))
					continue
				}
			}
			if r.expectedGVK != nil {
				if e, a := *r.expectedGVK, event.Object.GetObjectKind().GroupVersionKind(); e != a {
					utilruntime.HandleError(fmt.Errorf("%s: expected gvk %v, but watch event object had gvk %v", r.name, e, a))
					continue
				}
			}
			meta, err := meta.Accessor(event.Object)
			if err != nil {
				utilruntime.HandleError(fmt.Errorf("%s: unable to understand watch event %#v", r.name, event))
				continue
			}
			newResourceVersion := meta.GetResourceVersion()
			switch event.Type {
			case watch.Added:
				err := r.store.Add(event.Object)
				if err != nil {
					utilruntime.HandleError(fmt.Errorf("%s: unable to add watch event object (%#v) to store: %v", r.name, event.Object, err))
				}
			case watch.Modified:
				err := r.store.Update(event.Object)
				if err != nil {
					utilruntime.HandleError(fmt.Errorf("%s: unable to update watch event object (%#v) to store: %v", r.name, event.Object, err))
				}
			case watch.Deleted:
				// TODO: Will any consumers need access to the "last known
				// state", which is passed in event.Object? If so, may need
				// to change this.
				err := r.store.Delete(event.Object)
				if err != nil {
					utilruntime.HandleError(fmt.Errorf("%s: unable to delete watch event object (%#v) from store: %v", r.name, event.Object, err))
				}
			case watch.Bookmark:
				// A `Bookmark` means watch has synced here, just update the resourceVersion
			default:
				utilruntime.HandleError(fmt.Errorf("%s: unable to understand watch event %#v", r.name, event))
			}
			*resourceVersion = newResourceVersion
			r.setLastSyncResourceVersion(newResourceVersion)
			if rvu, ok := r.store.(ResourceVersionUpdater); ok {
				rvu.UpdateResourceVersion(newResourceVersion)
			}
			eventCount++
		}
	}

	watchDuration := r.clock.Since(start)
	if watchDuration < 1*time.Second && eventCount == 0 {
		return fmt.Errorf("very short watch: %s: Unexpected watch close - watch lasted less than a second and no items received", r.name)
	}
	klog.V(4).Infof("%s: Watch close - %v total %v items received", r.name, r.expectedTypeName, eventCount)
	return nil
}
```

### DeltaFIFO组件

DeltaFIFO可以分开理解为Delta和FIFO两个结构体，其中FIFO是一个先进先出的队列，实现了队列的基本操作，Pop,Add等等，而Delta是一个资源对象存储,可以保存资源对象的操作类型，比如说Add，Update，Sync等等。

DeltaFIFO与其他队列不同的地方在于，会保留所有关于资源对象（object）的操作类型，队列中会存在拥有不同操作类型的同一个资源对象，queue字段存储资源对象的key，而items则是一个map数据结构，其中key为资源对象，而value存储了对象的数组。

k8s.io/client-go/tools/cache/delta_fifo.go

![image-20211216142521855](https://github.com/JING21/K8S/raw/main/client-go/DeltaFIFO.png)

```go
type DeltaFIFO struct {
	// lock/cond protects access to 'items' and 'queue'.
	lock sync.RWMutex
	cond sync.Cond

	// `items` maps a key to a Deltas.
	// Each such Deltas has at least one Delta.
	items map[string]Deltas

	// `queue` maintains FIFO order of keys for consumption in Pop().
	// There are no duplicates in `queue`.
	// A key is in `queue` if and only if it is in `items`.
	queue []string

	// populated is true if the first batch of items inserted by Replace() has been populated
	// or Delete/Add/Update/AddIfNotPresent was called first.
	populated bool
	// initialPopulationCount is the number of items inserted by the first call of Replace()
	initialPopulationCount int

	// keyFunc is used to make the key used for queued item
	// insertion and retrieval, and should be deterministic.
	keyFunc KeyFunc

	// knownObjects list keys that are "known" --- affecting Delete(),
	// Replace(), and Resync()
	knownObjects KeyListerGetter

	// Used to indicate a queue is closed so a control loop can exit when a queue is empty.
	// Currently, not used to gate any of CRUD operations.
	closed bool

	// emitDeltaTypeReplaced is whether to emit the Replaced or Sync
	// DeltaType when Replace() is called (to preserve backwards compat).
	emitDeltaTypeReplaced bool
}
```

#### 生产者方法

DeltaFIFO本质上是一个先进先出的队列，拥有生产者和消费者，而生产者是Reflector调用add方法，而消费者是controller调用pop方法, add核心方法queueActionLocked()，

- 首先通过f.KeyOf()函数将传入的obeject解析计算出map的key值id
- 通过ID取到对应的item map中的value值 oldDeltas
- 将传入的obeject和对应的actionType构建为新的Delta元素添加至老的oldDeltas上，成为新的newDeltas，然后使用dedupDeltas()函数对其进行去重操作
- 当obejectID对应不存在时，在队列中添加该id，否则就更新item这个map中对应key值为id的value值，并广播通知所有消费者解除阻塞

k8s.io/client-go/tools/cache/delta_fifo.go

```go
// queueActionLocked appends to the delta list for the object.
// Caller must lock first.
func (f *DeltaFIFO) queueActionLocked(actionType DeltaType, obj interface{}) error {
	id, err := f.KeyOf(obj)
	if err != nil {
		return KeyError{obj, err}
	}
	oldDeltas := f.items[id]
	newDeltas := append(oldDeltas, Delta{actionType, obj})
	newDeltas = dedupDeltas(newDeltas)

	if len(newDeltas) > 0 {
		if _, exists := f.items[id]; !exists {
			f.queue = append(f.queue, id)
		}
		f.items[id] = newDeltas
		f.cond.Broadcast()
	} else {
		// This never happens, because dedupDeltas never returns an empty list
		// when given a non-empty list (as it is here).
		// If somehow it happens anyway, deal with it but complain.
		if oldDeltas == nil {
			klog.Errorf("Impossible dedupDeltas for id=%q: oldDeltas=%#+v, obj=%#+v; ignoring", id, oldDeltas, obj)
			return nil
		}
		klog.Errorf("Impossible dedupDeltas for id=%q: oldDeltas=%#+v, obj=%#+v; breaking invariant by storing empty Deltas", id, oldDeltas, obj)
		f.items[id] = newDeltas
		return fmt.Errorf("Impossible dedupDeltas for id=%q: oldDeltas=%#+v, obj=%#+v; broke DeltaFIFO invariant by storing empty Deltas", id, oldDeltas, obj)
	}
	return nil
}
```

#### 消费者方法

Pop()方法由消费者调用，从DeltaFIFO头部中取出最早进入队列的资源数据对象，需要传入PopProcessFunc函数，用于接收并处理对象的回调方法。

当队列为空时，f.cond.Wait()阻塞等待数据，只有收到f.cond.Broadcast()消息之后说明有数据被添加了，会解除阻塞状态，取出f.queue的头部函数，将obeject对象传入processFunc，由上层的消费者处理，如果processFunc处理出错，则会将对象重新存入队列中

```go
// Pop blocks until the queue has some items, and then returns one.  If
// multiple items are ready, they are returned in the order in which they were
// added/updated. The item is removed from the queue (and the store) before it
// is returned, so if you don't successfully process it, you need to add it back
// with AddIfNotPresent().
// process function is called under lock, so it is safe to update data structures
// in it that need to be in sync with the queue (e.g. knownKeys). The PopProcessFunc
// may return an instance of ErrRequeue with a nested error to indicate the current
// item should be requeued (equivalent to calling AddIfNotPresent under the lock).
// process should avoid expensive I/O operation so that other queue operations, i.e.
// Add() and Get(), won't be blocked for too long.
//
// Pop returns a 'Deltas', which has a complete list of all the things
// that happened to the object (deltas) while it was sitting in the queue.
func (f *DeltaFIFO) Pop(process PopProcessFunc) (interface{}, error) {
	f.lock.Lock()
	defer f.lock.Unlock()
	for {
		for len(f.queue) == 0 {
			// When the queue is empty, invocation of Pop() is blocked until new item is enqueued.
			// When Close() is called, the f.closed is set and the condition is broadcasted.
			// Which causes this loop to continue and return from the Pop().
			if f.closed {
				return nil, ErrFIFOClosed
			}

			f.cond.Wait()
		}
		id := f.queue[0]
		f.queue = f.queue[1:]
		depth := len(f.queue)
		if f.initialPopulationCount > 0 {
			f.initialPopulationCount--
		}
		item, ok := f.items[id]
		if !ok {
			// This should never happen
			klog.Errorf("Inconceivable! %q was in f.queue but not f.items; ignoring.", id)
			continue
		}
		delete(f.items, id)
		// Only log traces if the queue depth is greater than 10 and it takes more than
		// 100 milliseconds to process one item from the queue.
		// Queue depth never goes high because processing an item is locking the queue,
		// and new items can't be added until processing finish.
		// https://github.com/kubernetes/kubernetes/issues/103789
		if depth > 10 {
			trace := utiltrace.New("DeltaFIFO Pop Process",
				utiltrace.Field{Key: "ID", Value: id},
				utiltrace.Field{Key: "Depth", Value: depth},
				utiltrace.Field{Key: "Reason", Value: "slow event handlers blocking the queue"})
			defer trace.LogIfLong(100 * time.Millisecond)
		}
		err := process(item)
		if e, ok := err.(ErrRequeue); ok {
			f.addIfNotPresent(id, item)
			err = e.Err
		}
		// Don't need to copyDeltas here, because we're transferring
		// ownership to the caller.
		return item, err
	}
}
```

HandleDeltas()函数作为process的回调函数，当资源对象的操作类型为Added，Updated，Deleted时，将资源对象存储到Indexer中,并通过distribute函数将资源对象分发至SharedInformer，比如说分发给informer.AddEvenetHandler函数对资源时间进行处理的函数中进行处理。

k8s.io/client-go/tools/cache/shared_informer.go

```go
func (s *sharedIndexInformer) HandleDeltas(obj interface{}) error {
	s.blockDeltas.Lock()
	defer s.blockDeltas.Unlock()

	// from oldest to newest
	for _, d := range obj.(Deltas) {
		switch d.Type {
		case Sync, Replaced, Added, Updated:
			s.cacheMutationDetector.AddObject(d.Object)
			if old, exists, err := s.indexer.Get(d.Object); err == nil && exists {
				if err := s.indexer.Update(d.Object); err != nil {
					return err
				}

				isSync := false
				switch {
				case d.Type == Sync:
					// Sync events are only propagated to listeners that requested resync
					isSync = true
				case d.Type == Replaced:
					if accessor, err := meta.Accessor(d.Object); err == nil {
						if oldAccessor, err := meta.Accessor(old); err == nil {
							// Replaced events that didn't change resourceVersion are treated as resync events
							// and only propagated to listeners that requested resync
							isSync = accessor.GetResourceVersion() == oldAccessor.GetResourceVersion()
						}
					}
				}
				s.processor.distribute(updateNotification{oldObj: old, newObj: d.Object}, isSync)
			} else {
				if err := s.indexer.Add(d.Object); err != nil {
					return err
				}
				s.processor.distribute(addNotification{newObj: d.Object}, false)
			}
		case Deleted:
			if err := s.indexer.Delete(d.Object); err != nil {
				return err
			}
			s.processor.distribute(deleteNotification{oldObj: d.Object}, false)
		}
	}
	return nil
}
```

#### Resync机制

Resync机制将会把资源对象从本地存储Indexer同步到DeltaFIFO中，并将这些资源对象设置为Sync操作类型。Resync函数在Reflector中定期执行，执行周期由NewReflector中的resyncPeriod决定。具体代码实现如下，f.knowObjects.

```go
func (f *DeltaFIFO) syncKeyLocked(key string) error {
	obj, exists, err := f.knownObjects.GetByKey(key)
	if err != nil {
		klog.Errorf("Unexpected error %v during lookup of key %v, unable to queue object for sync", err, key)
		return nil
	} else if !exists {
		klog.Infof("Key %v does not exist in known objects store, unable to queue object for sync", key)
		return nil
	}

	// If we are doing Resync() and there is already an event queued for that object,
	// we ignore the Resync for it. This is to avoid the race, in which the resync
	// comes with the previous value of object (since queueing an event for the object
	// doesn't trigger changing the underlying store <knownObjects>.
	id, err := f.KeyOf(obj)
	if err != nil {
		return KeyError{obj, err}
	}
	if len(f.items[id]) > 0 {
		return nil
	}

	if err := f.queueActionLocked(Sync, obj); err != nil {
		return fmt.Errorf("couldn't queue object: %v", err)
	}
	return nil
}
```

### Indexer

Indexer是client-go用于存储资源对象，并自带索引功能的本地存储，Reflector将从DeltaFIFO中消费出来的资源对象存储至Indexer。Indexer中的数据与Etcd集群中的数据保持完全一致，从而可以避免每次从远程的Etcd集群中读取数据，这样可以减轻Apiserver和Etcd的压力

indexer=>Store=>cache(实现了Store接口)=>ThreadSafeStore 

以下为index的关键4个数据结构

 k8s.io/client-go/tools/cache/indexer.go

```go
// 用于计算一个对象的索引键集合
type IndexFunc func(obj interface{}) ([]string, error)

// 索引键与对象键集合的映射
type Index map[string]sets.String

// 索引器名称（或者索引分类）与 IndexFunc 的映射，相当于存储索引的各种分类
type Indexers map[string]IndexFunc

// 索引器名称与 Index 索引的映射
type Indices map[string]Index
```

```go
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
```

![image-20220525152754978](https://github.com/JING21/K8S/raw/main/client-go/index.png)



- IndexFunc：索引器函数，用于计算一个资源对象的索引值列表，上面示例是指定Annotation为索引值结果，当然我们也可以根据需求定义其他的，比如根据 Label 标签、Annotation 等属性来生成索引值列表。
- Index：存储数据，对于上面的示例，我们要查找某个Annotation下面的 Pod，那就要让 Pod 按照其命名空间进行索引，对应的 Index 类型就是 `map[Annotation]sets.pod`。
- Indexers：存储索引器，key 为索引器名称，value 为索引器的实现函数，上面的示例就是 `map["byUser"]MetaNamespaceIndexFunc`。
- Indices：存储缓存器，key 为索引器名称，value 为缓存的数据，对于上面的示例就是 `map["byUser"]map["ernie"]sets.pod`。

```json
// Indexers 就是包含的所有索引器(分类)以及对应实现
Indexers: {  
  "byUser": UsersIndexFunc,
}
// Indices 就是包含的所有索引分类中所有的索引数据
Indices: {
 "byUser": {  //namespace 这个索引分类下的所有索引数据
  "ernie": ["pod-1", "pod-2"],  // Index 就是一个索引键下所有的对象键列表
  "telsa": ["pod-3"]   // Index
 }
}
```

ThreadSafeMap的接口如下所示，以下threadSafeMap具体实现了该ThreadSafeMap接口，可以看到threadSafeMap具体包含了indexers和indices两个具体结构

```go
type ThreadSafeStore interface {
	Add(key string, obj interface{})
	Update(key string, obj interface{})
	Delete(key string)
	Get(key string) (item interface{}, exists bool)
	List() []interface{}
	ListKeys() []string
	Replace(map[string]interface{}, string)
	Index(indexName string, obj interface{}) ([]interface{}, error)
	IndexKeys(indexName, indexKey string) ([]string, error)
	ListIndexFuncValues(name string) []string
	ByIndex(indexName, indexKey string) ([]interface{}, error)
	GetIndexers() Indexers

	// AddIndexers adds more indexers to this store.  If you call this after you already have data
	// in the store, the results are undefined.
	AddIndexers(newIndexers Indexers) error
	// Resync is a no-op and is deprecated
	Resync() error
}

type threadSafeMap struct {
	lock  sync.RWMutex
	items map[string]interface{}

	// indexers maps a name to an IndexFunc
	indexers Indexers
	// indices maps a name to an Index
	indices Indices
}
```

threadSafeMap结构体实现了store接口的必要的方法，比如说Add,Update,Delete,Get等，其中最关键的函数为updateIndices(),根据indexer,获取对应存储索引器的名称和他对应的索引函数，当需要创建时仅需传入新的对象数据，而删除时仅需传入老的对象数据，而更新时则需要同时传入新旧两个对象数据，当oldObj不为空时，需要根据获取的索引函数计算出实际的老的索引值

```go
// updateIndices modifies the objects location in the managed indexes:
// - for create you must provide only the newObj
// - for update you must provide both the oldObj and the newObj
// - for delete you must provide only the oldObj
// updateIndices must be called from a function that already has a lock on the cache
func (c *threadSafeMap) updateIndices(oldObj interface{}, newObj interface{}, key string) {
	var oldIndexValues, indexValues []string
	var err error
	for name, indexFunc := range c.indexers {
		if oldObj != nil {
			oldIndexValues, err = indexFunc(oldObj)
		} else {
			oldIndexValues = oldIndexValues[:0]
		}
		if err != nil {
			panic(fmt.Errorf("unable to calculate an index entry for key %q on index %q: %v", key, name, err))
		}

		if newObj != nil {
			indexValues, err = indexFunc(newObj)
		} else {
			indexValues = indexValues[:0]
		}
		if err != nil {
			panic(fmt.Errorf("unable to calculate an index entry for key %q on index %q: %v", key, name, err))
		}

		index := c.indices[name]
		if index == nil {
			index = Index{}
			c.indices[name] = index
		}

		for _, value := range oldIndexValues {
			// We optimize for the most common case where index returns a single value.
			if len(indexValues) == 1 && value == indexValues[0] {
				continue
			}
			c.deleteKeyFromIndex(key, value, index)
		}
		for _, value := range indexValues {
			// We optimize for the most common case where index returns a single value.
			if len(oldIndexValues) == 1 && value == oldIndexValues[0] {
				continue
			}
			c.addKeyToIndex(key, value, index)
		}
	}
}

```

### WorkQueue

WorkQueue是Kubernetes中使用到的队列，被称作工作队列，具体代码如下

 vendor/k8s.io/client-go/util/workqueue/queue.go

```go
/*
Copyright 2015 The Kubernetes Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package workqueue

import (
	"sync"
	"time"

	"k8s.io/apimachinery/pkg/util/clock"
)

type Interface interface {
	Add(item interface{})
	Len() int
	Get() (item interface{}, shutdown bool)
	Done(item interface{})
	ShutDown()
	ShuttingDown() bool
}

// New constructs a new work queue (see the package comment).
func New() *Type {
	return NewNamed("")
}

func NewNamed(name string) *Type {
	rc := clock.RealClock{}
	return newQueue(
		rc,
		globalMetricsFactory.newQueueMetrics(name, rc),
		defaultUnfinishedWorkUpdatePeriod,
	)
}

func newQueue(c clock.Clock, metrics queueMetrics, updatePeriod time.Duration) *Type {
	t := &Type{
		clock:                      c,
		dirty:                      set{},
		processing:                 set{},
		cond:                       sync.NewCond(&sync.Mutex{}),
		metrics:                    metrics,
		unfinishedWorkUpdatePeriod: updatePeriod,
	}

	// Don't start the goroutine for a type of noMetrics so we don't consume
	// resources unnecessarily
	if _, ok := metrics.(noMetrics); !ok {
		go t.updateUnfinishedWorkLoop()
	}

	return t
}

const defaultUnfinishedWorkUpdatePeriod = 500 * time.Millisecond

// Type is a work queue (see the package comment).
type Type struct {
	// queue defines the order in which we will work on items. Every
	// element of queue should be in the dirty set and not in the
	// processing set.
	queue []t

	// dirty defines all of the items that need to be processed.
	dirty set

	// Things that are currently being processed are in the processing set.
	// These things may be simultaneously in the dirty set. When we finish
	// processing something and remove it from this set, we'll check if
	// it's in the dirty set, and if so, add it to the queue.
	processing set

	cond *sync.Cond

	shuttingDown bool

	metrics queueMetrics

	unfinishedWorkUpdatePeriod time.Duration
	clock                      clock.Clock
}

type empty struct{}
type t interface{}
type set map[t]empty

func (s set) has(item t) bool {
	_, exists := s[item]
	return exists
}

func (s set) insert(item t) {
	s[item] = empty{}
}

func (s set) delete(item t) {
	delete(s, item)
}

// Add marks item as needing processing.
func (q *Type) Add(item interface{}) {
	q.cond.L.Lock()
	defer q.cond.L.Unlock()
	if q.shuttingDown {
		return
	}
	if q.dirty.has(item) {
		return
	}

	q.metrics.add(item)

	q.dirty.insert(item)
	if q.processing.has(item) {
		return
	}

	q.queue = append(q.queue, item)
	q.cond.Signal()
}

// Len returns the current queue length, for informational purposes only. You
// shouldn't e.g. gate a call to Add() or Get() on Len() being a particular
// value, that can't be synchronized properly.
func (q *Type) Len() int {
	q.cond.L.Lock()
	defer q.cond.L.Unlock()
	return len(q.queue)
}

// Get blocks until it can return an item to be processed. If shutdown = true,
// the caller should end their goroutine. You must call Done with item when you
// have finished processing it.
func (q *Type) Get() (item interface{}, shutdown bool) {
	q.cond.L.Lock()
	defer q.cond.L.Unlock()
	for len(q.queue) == 0 && !q.shuttingDown {
		q.cond.Wait()
	}
	if len(q.queue) == 0 {
		// We must be shutting down.
		return nil, true
	}

	item, q.queue = q.queue[0], q.queue[1:]

	q.metrics.get(item)

	q.processing.insert(item)
	q.dirty.delete(item)

	return item, false
}

// Done marks item as done processing, and if it has been marked as dirty again
// while it was being processed, it will be re-added to the queue for
// re-processing.
func (q *Type) Done(item interface{}) {
	q.cond.L.Lock()
	defer q.cond.L.Unlock()

	q.metrics.done(item)

	q.processing.delete(item)
	if q.dirty.has(item) {
		q.queue = append(q.queue, item)
		q.cond.Signal()
	}
}

// ShutDown will cause q to ignore all new items added to it. As soon as the
// worker goroutines have drained the existing items in the queue, they will be
// instructed to exit.
func (q *Type) ShutDown() {
	q.cond.L.Lock()
	defer q.cond.L.Unlock()
	q.shuttingDown = true
	q.cond.Broadcast()
}

func (q *Type) ShuttingDown() bool {
	q.cond.L.Lock()
	defer q.cond.L.Unlock()

	return q.shuttingDown
}

func (q *Type) updateUnfinishedWorkLoop() {
	t := q.clock.NewTicker(q.unfinishedWorkUpdatePeriod)
	defer t.Stop()
	for range t.C() {
		if !func() bool {
			q.cond.L.Lock()
			defer q.cond.L.Unlock()
			if !q.shuttingDown {
				q.metrics.updateUnfinishedWork()
				return true
			}
			return false

		}() {
			return
		}
	}
}

```

WorkQueue支持3种队列，并且提供了3种接口，分别是Interface，DelayingInteface，RateLimitingInterface,分别是普通FIFO队列，延迟队列和限速队列

- **Interface**:FIFO队列接口，先进先出队列，并且有去重机制

- **DelayingInterface**:延迟队列接口，基于Interface接口实现封装，包含AddAfter(item interface{}, duration time.Duration)方法，延迟元素入队
- **RateLimitingInterface**：限速队列接口，基于DelayingInterface接口封装实现，包含AddRateLimited(item interface{})，Forget(item interface{})，NumRequeues(item interface{}) ，支持元素入队队列进行速率限制

#### FIFO队列

FIFO队列是最基本的队列，包含了队列的基本方法

 vendor/k8s.io/client-go/util/workqueue/queue.go

```go
type Interface interface {
	Add(item interface{})
	Len() int
	Get() (item interface{}, shutdown bool)
	Done(item interface{})
	ShutDown()
	ShutDownWithDrain()
	ShuttingDown() bool
}
```

- **Add**: 给队列添加任意类型的元素
- **Len**:当前队列的长度
- **Get**:获取当前队列的头部元素
- **Done**:标记队列中该元素已经被处理
- **Shutdown** ：关闭队列
- **ShutDownWithDrain**:优雅关闭
- **ShuttingDown**：查询队列是否关闭

 vendor/k8s.io/client-go/util/workqueue/queue.go

```go
type Type struct {
	// queue defines the order in which we will work on items. Every
	// element of queue should be in the dirty set and not in the
	// processing set.
	queue []t

	// dirty defines all of the items that need to be processed.
	dirty set

	// Things that are currently being processed are in the processing set.
	// These things may be simultaneously in the dirty set. When we finish
	// processing something and remove it from this set, we'll check if
	// it's in the dirty set, and if so, add it to the queue.
	processing set

	cond *sync.Cond

	shuttingDown bool
	drain        bool

	metrics queueMetrics

	unfinishedWorkUpdatePeriod time.Duration
	clock                      clock.WithTicker
}
```

FIFO队列的数据结构如上所示，主要包含了的queue，dirty，processing
