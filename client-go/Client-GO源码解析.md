# Client-GO源码解析

[toc]

### Informer机制

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

FIFO队列的数据结构如上所示，主要包含了的queue，dirty，processing。其中queue是实际存储元素的数据结构，是一个slice结构，这样就能确保queue的有序性，dirty字段，定义了所有需要被处理的元素，包含了去重的特性，保证了在并发的情况下，处理一个元素前即使被添加了多次，也只会被处理一次。processing字段则用于标记，标记一个元素是否正在处理，并且queue中包含的每个元素，都应该在dirty set中，但不在processing的set中。**ADD**方法的分析如下图所示：

![image-20220530152907506](https://github.com/JING21/K8S/raw/main/client-go/queue-p.png)

 vendor/k8s.io/client-go/util/workqueue/queue.go

```go
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

	item = q.queue[0]
	// The underlying array still exists and reference this object, so the object will not be garbage collected.
	q.queue[0] = nil
	q.queue = q.queue[1:]

	q.metrics.get(item)

	q.processing.insert(item)
	q.dirty.delete(item)

	return item, false
}

func (q *Type) Done(item interface{}) {
	q.cond.L.Lock()
	defer q.cond.L.Unlock()

	q.metrics.done(item)

	q.processing.delete(item)
	if q.dirty.has(item) {
		q.queue = append(q.queue, item)
		q.cond.Signal()
	} else if q.processing.len() == 0 {
		q.cond.Signal()
	}
}
```

通过**Add**方法分别向队列中插入元素1，2，3，**Add**方法判断dirty set中不存在该三个元素，同时，processing set中也不存在该三个元素，会将其加入dirty的set中。然后通过Get方法获取第一个进入队列的元素，获取queue[0]元素，即图中所示的元素1，（Get方法会阻塞，直到正常的返回所需元素，同时当队列为空并且shuttingDown信号部位为false时，也会进入等待状态），并且在processing中加入元素1（表示元素1正在处理中），同时会删除dirty set中对应的元素1，最后当处理完元素1后，会调用Done删除元素1，(如果元素1被再次标记dirty的话，会重新入队，重新进行处理）

下图为在并发情况下，如何保证一个元素哪怕被添加多次也只会处理一次的逻辑：

![image-20220530150336270](https://github.com/JING21/K8S/raw/main/client-go/queue-re.png)

在并发的场景下，假如goroutineA通过**Get**方法获取到了元素1，并且元素1被添加到processing的set中，同一时间，goroutineB通过**Add**方法加入了元素1，此时**Add**方法会先判断dirty set中不存在元素1，会将其加入dirty set，之后判断发现processing set中存在元素1，因此不会继续将其添加到queue队列中，而后，当goroutineA完成了处理调用了**Done**方法，判断发现dirty set中仍然存在元素1，则会重新将元素1入队，位于队列尾部。dirty和processing都是由Hash Map实现的，所以是无序的，仅保证去重。

#### 延迟队列

延迟队列是基于FIFO队列的接口进行封装，在原有的功能上增加了**AddAfter**方法，其作用是延迟一段时间后再将元素插入队列，具体接口定义如下所示：

 vendor/k8s.io/client-go/util/workqueue/delaying_queue.go

```go
type DelayingInterface interface {
	Interface
	// AddAfter adds an item to the workqueue after the indicated duration has passed
	AddAfter(item interface{}, duration time.Duration)
}


type delayingType struct {
	Interface

	// clock tracks time for delayed firing
	clock clock.Clock

	// stopCh lets us signal a shutdown to the waiting loop
	stopCh chan struct{}
	// stopOnce guarantees we only signal shutdown a single time
	stopOnce sync.Once

	// heartbeat ensures we wait no more than maxWait before firing
	heartbeat clock.Ticker

	// waitingForAddCh is a buffered channel that feeds waitingForAdd
	waitingForAddCh chan *waitFor

	// metrics counts the number of retries
	metrics retryMetrics
}

type waitFor struct {
	data    t
	readyAt time.Time
	// index in the priority queue (heap)
	index int
}
```

**AddAfter**方法会插入一个item（即要插入的元素）参数，并且附带上一个duration（延迟时间）参数，该duration参数会用于指定元素插入FIFO队列的延迟的时间。如果duration小于等0，则表示立马插入队列。

**delayingType**结构中最主要的字段就是**waitingForAddCh**，其默认初始大小为1000，而waitFor 结构包含实现了waitForProrityQueue这么一个优先队列，实现了一个堆。通过**AddAfter**方法插入元素时，是非阻塞状态的，只有当插入的元素大于或等于1000时，延迟队列才会进入阻塞状态。**waitingForAddCh **chan的数据通过goroutine运行的waitingLoop函数运行传输，运行情况如下图所示：

![image-20220530162439842](https://github.com/JING21/K8S/raw/main/client-go/delayingqueue.png)

 vendor/k8s.io/client-go/util/workqueue/delaying_queue.go

```go
// waitingLoop runs until the workqueue is shutdown and keeps a check on the list of items to be added.
func (q *delayingType) waitingLoop() {
	defer utilruntime.HandleCrash()

	// Make a placeholder channel to use when there are no items in our list
	never := make(<-chan time.Time)

	// Make a timer that expires when the item at the head of the waiting queue is ready
	var nextReadyAtTimer clock.Timer

	waitingForQueue := &waitForPriorityQueue{}
	heap.Init(waitingForQueue)

	waitingEntryByData := map[t]*waitFor{}

	for {
		if q.Interface.ShuttingDown() {
			return
		}

		now := q.clock.Now()

		// Add ready entries
		for waitingForQueue.Len() > 0 {
			entry := waitingForQueue.Peek().(*waitFor)
			if entry.readyAt.After(now) {
				break
			}

			entry = heap.Pop(waitingForQueue).(*waitFor)
			q.Add(entry.data)
			delete(waitingEntryByData, entry.data)
		}

		// Set up a wait for the first item's readyAt (if one exists)
		nextReadyAt := never
		if waitingForQueue.Len() > 0 {
			if nextReadyAtTimer != nil {
				nextReadyAtTimer.Stop()
			}
			entry := waitingForQueue.Peek().(*waitFor)
			nextReadyAtTimer = q.clock.NewTimer(entry.readyAt.Sub(now))
			nextReadyAt = nextReadyAtTimer.C()
		}

		select {
		case <-q.stopCh:
			return

		case <-q.heartbeat.C():
			// continue the loop, which will add ready items

		case <-nextReadyAt:
			// continue the loop, which will add ready items

		case waitEntry := <-q.waitingForAddCh:
			if waitEntry.readyAt.After(q.clock.Now()) {
				insert(waitingForQueue, waitingEntryByData, waitEntry)
			} else {
				q.Add(waitEntry.data)
			}

			drained := false
			for !drained {
				select {
				case waitEntry := <-q.waitingForAddCh:
					if waitEntry.readyAt.After(q.clock.Now()) {
						insert(waitingForQueue, waitingEntryByData, waitEntry)
					} else {
						q.Add(waitEntry.data)
					}
				default:
					drained = true
				}
			}
		}
	}
}
```

如图所示，将元素1放入**waitingForAddCh** chan中后，通过**waitingLoop**这个goroutine消费这个元素，当元素的延迟时间不大于当前时间时，说明该元素不会立马入队，而是需要延迟一段时间入队，此时会将元素先放在优秀队列（实际上是一个堆）。当元素的延迟时间大于当前时间，则表明该元素需要直接入队，加入FIFO队列中。同时，**waitingLoop**会遍历优先队列**waitingForQueue**（waitForPriorityQueue类型），根据上述逻辑验证延迟时间和当前时间，进行入队操作和从优秀队列去除的操作。

#### 限速队列

限速队列接口（RateLimitingInterface）是基于延迟队列接口进行封装，增加了**AddRateLimited**,**Forget**,**NumRequeus**方法。限速队列的重点在于其提供了4种不同的限速算法接口（RateLimiter），其核心原理是，限速队列利用延迟队列的特性，延迟某个队列的入队时间，从而达到限速的目的，其中限速算法的接口也如下所示:

 vendor/k8s.io/client-go/util/workqueue/rate_limiting_queue.go

```go
type RateLimitingInterface interface {
	DelayingInterface

	// AddRateLimited adds an item to the workqueue after the rate limiter says it's ok
	AddRateLimited(item interface{})

	// Forget indicates that an item is finished being retried.  Doesn't matter whether it's for perm failing
	// or for success, we'll stop the rate limiter from tracking it.  This only clears the `rateLimiter`, you
	// still have to call `Done` on the queue.
	Forget(item interface{})

	// NumRequeues returns back how many times the item was requeued
	NumRequeues(item interface{}) int
}
```

 vendor/k8s.io/client-go/util/workqueue/default_rate_limiters_queue.go

```go
type RateLimiter interface {
	// When gets an item and gets to decide how long that item should wait
	When(item interface{}) time.Duration
	// Forget indicates that an item is finished being retried.  Doesn't matter whether it's for failing
	// or for success, we'll stop tracking it
	Forget(item interface{})
	// NumRequeues returns back how many failures the item has had
	NumRequeues(item interface{}) int
}
```

- **When**: 获取指定元素的应该等待时间
- **Forgert**：无论是否成功，释放该元素，清空该元素的排队数
- **NumRequeues**:获取该元素的失败数（排队数）

其中限速周期是指，一个限速周期是指一个元素从执行**AddRateLimited**方法到执行完成**Forget**方法之间的时间，如果该元素被**Forget**方法处理完成，则清空排队数。Client-go提供了默认4种限速算法，应对不同场景：

- 令牌桶算法（BuckRateLimiter）
- 排队指数算法（ItemExponentialFailureRateLimiter）
- 计算器算法（ItemFastSlowRateLimiter）
- 混合模式（MaxOfRateLimiter）

##### 令牌桶算法

令牌桶算法是通过golang的第三方库golang.org/x/time/rate实现的。具体的数据结构定义如下：

golang.org/x/time/rate/rate.go

```golang
type Limiter struct {
	mu     sync.Mutex
	limit  Limit
	burst  int
	tokens float64
	// last is the last time the limiter's tokens field was updated
	last time.Time
	// lastEvent is the latest time of a rate-limited event (past or future)
	lastEvent time.Time
}
```

令牌桶算法实现一个存储**token**的桶结构，初始时桶为空，token会以固定速率被填进桶里，直至填满为止，而多余的token则会被废弃。然后每个元素都会从令牌桶中得到一个token，只有得到token的元素才能通过校验（accept），而没有得到token的元素需要等待获取到token。令牌桶算法通过控制发放token来达到限速的目的。

![image-20220531102137916](https://github.com/JING21/K8S/raw/main/client-go/bucketRateLimiter.png)

 vendor/k8s.io/client-go/util/workqueue/default_rate_limiters_queue.go

```go
func DefaultControllerRateLimiter() RateLimiter {
	return NewMaxOfRateLimiter(
		NewItemExponentialFailureRateLimiter(5*time.Millisecond, 1000*time.Second),
		// 10 qps, 100 bucket size.  This is only for retry speed and its only the overall factor (not per item)
		&BucketRateLimiter{Limiter: rate.NewLimiter(rate.Limit(10), 100)},
	)
}
```

golang.org/x/time/rate/rate.go

```go
// NewLimiter returns a new Limiter that allows events up to rate r and permits
// bursts of at most b tokens.
func NewLimiter(r Limit, b int) *Limiter {
	return &Limiter{
		limit: r,
		burst: b,
	}
}
```

Workqueue在默认情况下会实例化令牌桶，rate.NewLimiter(r,b)，实例化limiter后传入了r，b两个参数，其中r表示每秒放入桶中的token数量，b表示桶的大小（能存放token的最大值）。假设在一个限速周期内添加了500个元素，通过r.Limiter.Reserve().Delay()可以得到具体某个元素需要等待的时长，那么前b个（例子中为100）100个元素会直接取到token，直接处理。而后面的元素item100开始，会延迟item100/100ms,item101/200ms,item102/300ms。

 vendor/k8s.io/client-go/util/workqueue/default_rate_limiters_queue.go

```go
func (r *BucketRateLimiter) When(item interface{}) time.Duration {
	return r.Limiter.Reserve().Delay()
}
```

golang.org/x/time/rate/rate.go

```go
// Delay is shorthand for DelayFrom(time.Now()).
func (r *Reservation) Delay() time.Duration {
	return r.DelayFrom(time.Now())
}
```

##### 排队指数算法

排队指数算法将相同元素的排队数作为指数，排队数增大时，速率呈指数级增长，但最大值不会超过**maxDelay**。但是该统计是以一个限速周期内进行计算的，即执行**AddRateLimited**方法至**Forget**方法之间的时间，如果该元素被**Forget**方法处理完，则清空排队数，核心代码如下：

 vendor/k8s.io/client-go/util/workqueue/default_rate_limiters_queue.go

```go
func (r *ItemExponentialFailureRateLimiter) When(item interface{}) time.Duration {
	r.failuresLock.Lock()
	defer r.failuresLock.Unlock()

	exp := r.failures[item]
	r.failures[item] = r.failures[item] + 1

	// The backoff is capped such that 'calculated' value never overflows.
	backoff := float64(r.baseDelay.Nanoseconds()) * math.Pow(2, float64(exp))
	if backoff > math.MaxInt64 {
		return r.maxDelay
	}

	calculated := time.Duration(backoff)
	if calculated > r.maxDelay {
		return r.maxDelay
	}

	return calculated
}
```

其中主要的字段包含**failures**，**baseDelay**，**maxDelay**。其中**failures**字段表示元素重新入队次数，即排队数，每当**AddRateLimited**方法插入一个新的元素时，**failures**即会累加。**baseDelay**字段是最初的限速单位，为5ms，而默认**maxDelay**字段是最大限速单位，为1000s。

限速队列利用延迟队列，延迟入队的特性，延迟相同的多个元素入队，从而达到限速的目的。在同一限速周期内，如果不存在相同的元素，那么所有的元素的延迟时间均为**baseDelay**,但是如果存在相同元素，相同元素延迟时间呈指数增长。

使用默认的**baseDelay**是1*time.Millisecond，**maxDelay**是1000 *time.Second。假设在一个限速周期内，插入10个相同的元素，第一个元素设置的延迟时间即为**baseDelay**(1ms),第二个则为2ms,第三个则为4ms，第四个则为8ms，第五个则为16ms.....以此类推第十个即为512ms，最长的延迟时间部的大过**maxDelay**（1000s）。

##### 计数器算法

计数器算法的原理是，限制的一段时间内允许通过的元素数量，例如1分钟只能通过50个元素，每插入一个元素，计数器就自增1，当计数器达到阈值50时并且还在限速周期内，就不允许元素继续通过。

 vendor/k8s.io/client-go/util/workqueue/default_rate_limiters_queue.go

```go
type ItemFastSlowRateLimiter struct {
	failuresLock sync.Mutex
	failures     map[interface{}]int

	maxFastAttempts int
	fastDelay       time.Duration
	slowDelay       time.Duration
}

var _ RateLimiter = &ItemFastSlowRateLimiter{}

func NewItemFastSlowRateLimiter(fastDelay, slowDelay time.Duration, maxFastAttempts int) RateLimiter {
	return &ItemFastSlowRateLimiter{
		failures:        map[interface{}]int{},
		fastDelay:       fastDelay,
		slowDelay:       slowDelay,
		maxFastAttempts: maxFastAttempts,
	}
}

func (r *ItemFastSlowRateLimiter) When(item interface{}) time.Duration {
	r.failuresLock.Lock()
	defer r.failuresLock.Unlock()

	r.failures[item] = r.failures[item] + 1

	if r.failures[item] <= r.maxFastAttempts {
		return r.fastDelay
	}

	return r.slowDelay
}
```

**ItemFastSlowRateLimiter**包含了4个主要字段，**failures**，**fastDelay**，**slowDelay**，**maxFastAttempts**。**failures**是用于统计元素排队数的，每当有新元素插入时，该字段会加1，而**fastDelay**，**slowDelay**则是用于定义fast,slow速率的，当排队数不大于**maxFastAttempts**时，就会使用**fastDelay**速率，否则就使用**slowDelay**速率。

假设**fastDelay**是5* time.Millisecond,而**slowDelay**是20* time.Millisecond, **maxFastAttempts**是10，在一个限速周期内，当调用**AddRateLimited**方法插入第十一个相同元素时，前十个会用**fastDelay**速率，而从11个开始则会使用**slowDelay**速率。

##### 混合模式

混合模式即使用多种限速算法混合使用，如**DefaultControllerRateLimiter**使用了排队指数算法和令牌桶算法

 vendor/k8s.io/client-go/util/workqueue/default_rate_limiters_queue.go

```go
type MaxOfRateLimiter struct {
	limiters []RateLimiter
}

func DefaultControllerRateLimiter() RateLimiter {
	return NewMaxOfRateLimiter(
		NewItemExponentialFailureRateLimiter(5*time.Millisecond, 1000*time.Second),
		// 10 qps, 100 bucket size.  This is only for retry speed and its only the overall factor (not per item)
		&BucketRateLimiter{Limiter: rate.NewLimiter(rate.Limit(10), 100)},
	)
}
```



