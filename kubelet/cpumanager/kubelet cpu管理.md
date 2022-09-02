[TOC]

# Kubelet cpu绑核功能 

## 背景

openstack容器化组件容器化过程中，需要在部署组件的节点预留cpu给系统组件使用，保证容器不使用预留cpu。相关场景的问题是：reserved-cpu保留后，仅对Guaranteed类型的pod有效，同时cpu相关qos必须为整型参数。

具体的需求如下：

1. 所有服务 yaml 文件无需修改（既不需要增加 resources：limits(memory+cpu) + requests(memory+cpu) 配置）
2. 同节点上所有 Pod 共用配置的 reserved-cpus 之外的核池。

## Kubernetes相关参数功能

### pod类型

Kubernetes中的pod拥有三种QoS类型，分别是：Guaranteed，Burstable，BestEffort

- Guaranteed类型：

​	要求pod中声明了Resources的limit和requests类型，同时还要求两者的值必须相等

```yaml
apiVersion: v1
kind: Pod
metadata:
  name: cpu-test
spec:
  containers:
  - args:
    - sleep
    - "3600"
    image: registry.paas/cmss/busybox:1.24
    name: busybox-pod1
    resources:
      limits:
        cpu: "3"
        memory: 500Mi
      requests:
        cpu: "3"
        memory: "500Mi"
  nodeName: node2
```

- Burstable类型：

​	要求pod中声明了Resources的limit和requests类型，但是request可以小于limit，或者cpu和内存资源仅声明其中之一，就会是   	Burstable类型的Qos

```yaml
apiVersion: v1
kind: Pod
metadata:
  name: cpu-test
spec:
  containers:
  - args:
    - sleep
    - "3600"
    image: registry.paas/cmss/busybox:1.24
    name: busybox-pod1
    resources:
      limits:
        cpu: "3"    
      requests:
        cpu: "2"
  nodeName: node2
```

- BestEffort类型

​	 pod的声明中未申请Resources对应的具体的limit和requests类型，就会定义Qos类型为BestEffort

```yaml
apiVersion: v1
kind: Pod
metadata:
  name: cpu-test
spec:
  containers:
  - args:
    - sleep
    - "3600"
    image: registry.paas/cmss/busybox:1.24
    name: busybox-pod1
  nodeName: node2
```

- Best-Effort 类型的pods：系统用完了全部内存时，该类型pods会最先被kill掉。

- Burstable类型pods：系统用完了全部内存，且没有Best-Effort container可以被kill时，该类型pods会被kill掉。

- Guaranteed pods：系统用完了全部内存、且没有Burstable与Best-Effort container可以被kill，该类型的pods会被kill掉。

  如果pod进程因使用超过预先设定的*limites*而非Node资源紧张情况，系统倾向于在其原所在的机器上重启该container或本机或其他重新创建一个pod。

### kubelet相关参数

Kubernetes中kubelet负责容器的生命周期管理。同时提供了具体参数，--reserved-cpus，--system-reserved，--kube-reserved,(其中reserved-cpus优先级高于system-reserved和kube-reserved，会进行覆盖)对节点资源进行预留。

cpu-manager作为kubelet的containerManager的一种，负责容器的cpu资源的管理，其中kubelet可以指定参数--cpuManagerPolicy,当前支持2种cpu策略。分别是1.none和2.static

- none类型

​	Kubernetes使用默认的cpu亲和性方案，通过Linux的CFS调度器来管理Guaranteed pods的 CPU 使用以及限制。所谓的CFS（Completely Fair Scheduler），就如字面意思所述，完全公平调度。实际意义即为在一个特定的调度周期内，保证所有的调度的进程都可以被执行，每个进程的vruntime（并不是进程的实际占用cpu时间，是剔除了权重影响的cpu使用时间，而实际的cpu使用时间是会根据进程的优先级权重进行扩缩的）的计算公式如下：

```
进程运行时间 = 调度周期 * 进程权重 / 所有进程权重之和

vruntime = 进程运行时间 * NICE_0_LOAD / 进程权重 = (调度周期 * 进程权重 / 所有进程总权重) * NICE_0_LOAD / 进程权重 = 调度周期 * NICE_0_LOAD / 所有进程总权重 
```

其中以下的内核参数是CFS调度有关的

- /proc/sys/kernel/sched_min_granularity_ns，表示进程最少运行时间，防止进程随意的切换
- /proc/sys/kernel/sched_nr_migrate，表示多CPU情况下进行负载均衡时，一次最多移动多少个进程到另一个CPU上
- /proc/sys/kernel/sched_wakeup_granularity_ns，表示进程被唤醒后至少应该运行的时间，这个数值越小，那么发生抢占的概率也就越高
- /proc/sys/kernel/sched_latency_ns，表示一个运行队列所有进程运行一次的时间长度(正常情况下的队列调度周期，P)
- sched_nr_latency，这个参数是内核内部参数，无法直接设置，是通过sched_latency_ns/sched_min_granularity_ns这个公式计算出来的；在实际运行中，如果队列排队进程数 nr_running > sched_nr_latency，则调度周期就不是sched_latency_ns，而是P = sched_min_granularity_ns * nr_running，如果 nr_running <= sched_nr_latency，则 P = sched_latency_ns

目前环境的参数如下：

```bash
[root@node2 kernel]# cat /proc/sys/kernel/sched_min_granularity_ns
10000000
[root@node2 kernel]# cat /proc/sys/kernel/sched_nr_migrate
32
[root@node2 kernel]# cat /proc/sys/kernel/sched_wakeup_granularity_ns
15000000
[root@node2 kernel]# cat /proc/sys/kernel/sched_latency_ns
24000000
```

其中可以算出sched_nr_latency = sched_latency_ns/sched_min_granularity_ns = 24000000/10000000 = 2.4，表示当进程队列中有2.4个在等待时，开始保证每个进程至少运行10000000ns（即10ms）

观察对应的进程cpu使用情况，其中se.sum_exec_runtime是实际占用cpu的时间:

```bash
/ # cat /proc/6/sched
sh (6, #threads: 1)
-------------------------------------------------------------------
se.exec_start                                :     257239294.640742
se.vruntime                                  :            17.932870
se.sum_exec_runtime                          :            25.294106
se.nr_migrations                             :                   16
nr_switches                                  :                  107
nr_voluntary_switches                        :                  107
nr_involuntary_switches                      :                    0
se.load.weight                               :              1048576
se.runnable_weight                           :              1048576
se.avg.load_sum                              :                  214
se.avg.runnable_load_sum                     :                  214
se.avg.util_sum                              :               219136
se.avg.load_avg                              :                    0
se.avg.runnable_load_avg                     :                    0
se.avg.util_avg                              :                    0
se.avg.last_update_time                      :      257239294640128
se.avg.util_est.ewma                         :                    8
se.avg.util_est.enqueued                     :                    0
policy                                       :                    0
prio                                         :                  120
clock-delta                                  :                   47
mm->numa_scan_seq                            :                    0
numa_pages_migrated                          :                    0
numa_preferred_nid                           :                   -1
total_numa_faults                            :                    0
current_node=0, numa_group_id=0
numa_faults node=0 task_private=0 task_shared=0 group_private=0 group_shared=0
/ # cat /proc/6/sched
sh (6, #threads: 1)
-------------------------------------------------------------------
se.exec_start                                :     257514956.566420
se.vruntime                                  :             4.333984
se.sum_exec_runtime                          :            25.608097
se.nr_migrations                             :                   17
nr_switches                                  :                  110
nr_voluntary_switches                        :                  110
nr_involuntary_switches                      :                    0
se.load.weight                               :              1048576
se.runnable_weight                           :              1048576
se.avg.load_sum                              :                  183
se.avg.runnable_load_sum                     :                  183
se.avg.util_sum                              :               187416
se.avg.load_avg                              :                    3
se.avg.runnable_load_avg                     :                    3
se.avg.util_avg                              :                    3
se.avg.last_update_time                      :      257514956565504
se.avg.util_est.ewma                         :                    8
se.avg.util_est.enqueued                     :                    0
policy                                       :                    0
prio                                         :                  120
clock-delta                                  :                   50
mm->numa_scan_seq                            :                    0
numa_pages_migrated                          :                    0
numa_preferred_nid                           :                   -1
total_numa_faults                            :                    0
current_node=0, numa_group_id=0
numa_faults node=0 task_private=0 task_shared=0 group_private=0 group_shared=0
```

当设置了pod的Resources的limit和requests的属性如下：

```yaml
apiVersion: v1
kind: Pod
metadata:
  name: cpu-test
spec:
  containers:
  - args:
    - sleep
    - "3600"
    image: registry.paas/cmss/busybox:1.24
    name: busybox-pod1
    resources:
      limits:
        cpu: "3.5"
        memory: 500Mi
      requests:
        cpu: "3.5"
        memory: "500Mi"
  nodeName: node2
```

可以观察到对应的Resources的参数被设置成了对应的cgroup参数：

```bash
/ # cat /sys/fs/cgroup/cpu/cpu.shares 
3584
/ # cat /sys/fs/cgroup/cpu/cpu.cfs_quota_us
350000
/ # cat /sys/fs/cgroup/cpu/cpu.cfs_period_us
100000
/ # cat /proc/sys/kernel/sched_latency_ns
24000000
```

cpu.shares即为request值:3.5*1024=3584，cpu.cfs_quota_us即为limits值:350000，因此正常情况下，cfs调度器的调度时周期是24ms，而重分配周期是100ms，并且该pod在一个重分配周期中最多可占用35ms，并且有压力的情况下，pod可以占据cpu的share比例是3584

![image-20220729102044974](https://github.com/JING21/K8S/raw/main/kubelet/cpumanager/cfs调度.jpg)

在上图的例子中，

- CFS调度周期为24ms，正常负载的情况下，进程队列中的进程每隔24ms就会被保证执行一次
- CFS重分配周期为100ms，用于保证一个进程的limits设置表示每个重分配周期（100ms该例子中）可以占用cpu的时间，多核系统中，max(limit)=CFS重分配周期(100ms)*CPU核数
- 因为进程A和B的share值一样，所以在cpu中可以占用同样的资源
- 因为cpu-quota分别为35ms和20ms，所以两个进程在一个重分配周期中，最多占有cpu35ms和20ms
- 在前面的2个CFS调度周期内，进程A和B由于share值是一样的，所以每个CFS调度内(24ms)，进程A和B都会占用10ms
- 在第3个CFS调度周期结束的时候，在本CFS重分配周期内，进程B已经占用了20ms，在剩下的2个CFS调度周期即52ms内，进程B都会被限流，一直到下一个CFS重分配周期内，进程B才可以继续占用CPU
- 在3-5个CFS调度周期内，进程B会被限流，所以进程A在第三个调度周期内可以拥有完整的cpu资源，但是进程A也只能占用cpu资源35ms，所以在4-5周期内也会被限流。

所以pod的limit和进程在cfs调度周期内是密切相关的，比如说200m的limit配置，导致进程在cpu的分配周期内100ms只能占用20ms，会出现问题。

如果不指定该参数则默认使用none类型，否则可以指定参数为static，指定了static的调度策略以及reserved-cpus保留策略，生成Guaranteed类型的pod，则可以达到独占cpu的效果。

- static策略会管理一个CPU共享资源池（Shared CPU Pool），该共享资源池包含节点上所有的CPU资源，可用且可以独占的CPU数量等于节点的CPU总量减去reserved-cpus或者--system-reserved或者--kube-reserved的资源。
- 通过参数预留的CPU是整数的形式，仅支持完整的单核，不能支持0.5c，0.2c这样的参数。共享池的cpu是Qos类型BestEffort 和 Burstable pod 运行的CPU 集合。同时如果Guaranteed类型的pod声明了非整数型的CPU也会运行在共享池的cpu上，只有指定了正整数类型的CPU的Guaranteed类型的pod才会独占完整CPU。
- 参照以下的yaml文件配置：

```yaml
apiVersion: v1
kind: Pod
metadata:
  name: cpu-test
spec:
  containers:
  - args:
    - sleep
    - "3600"
    image: registry.paas/cmss/busybox:1.24
    name: busybox-pod1
    resources:
      limits:
        cpu: "3"
        memory: 500Mi
      requests:
        cpu: "3"
        memory: "500Mi"
  nodeName: node2
```

```bash
[root@node2 9681336d6f6238ceab6a4dd5998cf1013dd04015f57bab99b0112af556b5ed7b]# for i in `ls cpuset.cpus tasks` ; do echo -n "$i "; cat $i ; done
cpuset.cpus 4-6
tasks 5560
[root@node2 9681336d6f6238ceab6a4dd5998cf1013dd04015f57bab99b0112af556b5ed7b]# cd ..
[root@node2 pod07550085-3206-4885-bb46-ebb97b8e2200]# ls
10513fe58e11396ba00ee4feeb61073c96eb23f06f912c26c02bddfe4361cae7  cpuset.memory_migrate
9681336d6f6238ceab6a4dd5998cf1013dd04015f57bab99b0112af556b5ed7b  cpuset.memory_pressure
cgroup.clone_children                                             cpuset.memory_spread_page
cgroup.procs                                                      cpuset.memory_spread_slab
cpuset.cpu_exclusive                                              cpuset.mems
cpuset.cpus                                                       cpuset.sched_load_balance
cpuset.effective_cpus                                             cpuset.sched_relax_domain_level
cpuset.effective_mems                                             notify_on_release
cpuset.mem_exclusive                                              tasks
cpuset.mem_hardwall
[root@node2 pod07550085-3206-4885-bb46-ebb97b8e2200]# cd10513fe58e11396ba00ee4feeb61073c96eb23f06f912c26c02bddfe4361cae7
-bash: cd10513fe58e11396ba00ee4feeb61073c96eb23f06f912c26c02bddfe4361cae7: 未找到命令
[root@node2 pod07550085-3206-4885-bb46-ebb97b8e2200]# ls
10513fe58e11396ba00ee4feeb61073c96eb23f06f912c26c02bddfe4361cae7  cpuset.memory_migrate
9681336d6f6238ceab6a4dd5998cf1013dd04015f57bab99b0112af556b5ed7b  cpuset.memory_pressure
cgroup.clone_children                                             cpuset.memory_spread_page
cgroup.procs                                                      cpuset.memory_spread_slab
cpuset.cpu_exclusive                                              cpuset.mems
cpuset.cpus                                                       cpuset.sched_load_balance
cpuset.effective_cpus                                             cpuset.sched_relax_domain_level
cpuset.effective_mems                                             notify_on_release
cpuset.mem_exclusive                                              tasks
cpuset.mem_hardwall
[root@node2 pod07550085-3206-4885-bb46-ebb97b8e2200]# cd 10513fe58e11396ba00ee4feeb61073c96eb23f06f912c26c02bddfe4361cae7
[root@node2 10513fe58e11396ba00ee4feeb61073c96eb23f06f912c26c02bddfe4361cae7]# for i in `ls cpuset.cpus tasks` ; do echo -n "$i "; cat $i ; done
cpuset.cpus 0-7
tasks 5396
(reverse-i-search)`grep ': history |^Cep cpu
[root@node2 10513fe58e11396ba00ee4feeb61073c96eb23f06f912c26c02bddfe4361cae7]# grep 'processor' /proc/cpuinfo | wc -l
8
[root@master cpu]# kubectl describe po cpu-test
Name:         cpu-test
Namespace:    default
Priority:     0
Node:         node2/100.73.61.22
Start Time:   Fri, 29 Jul 2022 14:19:26 +0800
Labels:       <none>
Annotations:  cni.projectcalico.org/containerID: 10513fe58e11396ba00ee4feeb61073c96eb23f06f912c26c02bddfe4361cae7
              cni.projectcalico.org/podIP: 10.244.104.61/32
              cni.projectcalico.org/podIPs: 10.244.104.61/32
              k8s.v1.cni.cncf.io/network-status:
                [{
                    "name": "k8s-pod-network",
                    "ips": [
                        "10.244.104.61"
                    ],
                    "default": true,
                    "dns": {}
                }]
              k8s.v1.cni.cncf.io/networks-status:
                [{
                    "name": "k8s-pod-network",
                    "ips": [
                        "10.244.104.61"
                    ],
                    "default": true,
                    "dns": {}
                }]
              ovn.kubernetes.io/allocated: true
              ovn.kubernetes.io/cidr: 10.16.0.0/16,fd00:10:16::/64
              ovn.kubernetes.io/gateway: 10.16.0.1,fd00:10:16::1
              ovn.kubernetes.io/ip_address: 10.16.0.10,fd00:10:16::a
              ovn.kubernetes.io/logical_router: ovn-cluster
              ovn.kubernetes.io/logical_switch: ovn-default
              ovn.kubernetes.io/mac_address: 00:00:00:4D:3C:02
              ovn.kubernetes.io/pod_nic_type: veth-pair
              ovn.kubernetes.io/routed: true
Status:       Running
IP:           10.244.104.61
IPs:
  IP:  10.244.104.61
Containers:
  busybox-pod1:
    Container ID:  docker://3d476202aeb46d4431e9073e521103cda36d0262f2def11b41d56d89e78d5c8c
    Image:         registry.paas/cmss/busybox:1.24
    Image ID:      docker-pullable://registry.paas/cmss/busybox@sha256:f73ae051fae52945d92ee20d62c315306c593c59a429ccbbdcba4a488ee12269
    Port:          <none>
    Host Port:     <none>
    Args:
      sleep
      3600
    State:          Running
      Started:      Fri, 29 Jul 2022 15:19:27 +0800
    Last State:     Terminated
      Reason:       Completed
      Exit Code:    0
      Started:      Fri, 29 Jul 2022 14:19:27 +0800
      Finished:     Fri, 29 Jul 2022 15:19:27 +0800
    Ready:          True
    Restart Count:  1
    Limits:
      cpu:     3
      memory:  500Mi
    Requests:
      cpu:        3
      memory:     500Mi
    Environment:  <none>
    Mounts:
      /var/run/secrets/kubernetes.io/serviceaccount from kube-api-access-6z7rt (ro)
Conditions:
  Type              Status
  Initialized       True
  Ready             True
  ContainersReady   True
  PodScheduled      True
Volumes:
  kube-api-access-6z7rt:
    Type:                    Projected (a volume that contains injected data from multiple sources)
    TokenExpirationSeconds:  3607
    ConfigMapName:           kube-root-ca.crt
    ConfigMapOptional:       <nil>
    DownwardAPI:             true
QoS Class:                   Guaranteed
Node-Selectors:              <none>
Tolerations:                 node.kubernetes.io/not-ready:NoExecute op=Exists for 300s
                             node.kubernetes.io/unreachable:NoExecute op=Exists for 300s
Events:
  Type    Reason   Age                From     Message
  ----    ------   ----               ----     -------
  Normal  Pulled   39m (x2 over 99m)  kubelet  Container image "registry.paas/cmss/busybox:1.24" already present on machine
  Normal  Created  39m (x2 over 99m)  kubelet  Created container busybox-pod1
  Normal  Started  39m (x2 over 99m)  kubelet  Started container busybox-pod1
  
[root@node2 10513fe58e11396ba00ee4feeb61073c96eb23f06f912c26c02bddfe4361cae7]# cat /var/lib/kubelet/config.yaml
apiVersion: kubelet.config.k8s.io/v1beta1
authentication:
  anonymous:
    enabled: false
  webhook:
    cacheTTL: 0s
    enabled: true
  x509:
    clientCAFile: /etc/kubernetes/pki/ca.crt
authorization:
  mode: Webhook
  webhook:
    cacheAuthorizedTTL: 0s
    cacheUnauthorizedTTL: 0s
cgroupDriver: cgroupfs
clusterDNS:
- 10.96.0.10
clusterDomain: cluster.local
cpuManagerReconcilePeriod: 0s
evictionPressureTransitionPeriod: 0s
fileCheckFrequency: 0s
healthzBindAddress: 127.0.0.1
healthzPort: 10248
httpCheckFrequency: 0s
imageMinimumGCAge: 0s
kind: KubeletConfiguration
logging: {}
nodeStatusReportFrequency: 0s
nodeStatusUpdateFrequency: 0s
rotateCertificates: true
runtimeRequestTimeout: 0s
shutdownGracePeriod: 0s
shutdownGracePeriodCriticalPods: 0s
staticPodPath: /etc/kubernetes/manifests
streamingConnectionIdleTimeout: 0s
syncFrequency: 0s
volumeStatsAggPeriod: 0s
reservedSystemCPUs: "0-3"
cpuManagerPolicy: static
```

从结果可以看出，该pod的类型是Guaranteed类型，同时因为kubelet配置了reserved-cpus，保留了4个核，(reservedSystemCPUs: "0-3")，所以该pod的业务容器只会使用cpuset.cpus 4-6,不会使用0-3这三个核，但是每个pod的根容器（pause容器）则会使用到预留资源的核0-3，会默认随机使用0-7所有的cpu。

所以目前的能满足的情况是，在pod配置为Guaranteed类型的Qos的pod后，并且cpu使用必须声明为整数类型，才能独占cpu，不使用保留的cpus。

### Kubelet相关代码分析

kubelet的代码结构使用传统的golang结构cmd/pkg结构，启动代码中与cpu-manager相关的如下所示(未包含全部)：

cmd/kubelet/app/server.go

```golang
func run(ctx context.Context, s *options.KubeletServer, kubeDeps *kubelet.Dependencies, featureGate featuregate.FeatureGate) (err error) {
    ···
		var reservedSystemCPUs cpuset.CPUSet
		if s.ReservedSystemCPUs != "" {
			// is it safe do use CAdvisor here ??
			machineInfo, err := kubeDeps.CAdvisorInterface.MachineInfo()
			if err != nil {
				// if can't use CAdvisor here, fall back to non-explicit cpu list behavor
				klog.InfoS("Failed to get MachineInfo, set reservedSystemCPUs to empty")
				reservedSystemCPUs = cpuset.NewCPUSet()
			} else {
				var errParse error
				reservedSystemCPUs, errParse = cpuset.Parse(s.ReservedSystemCPUs)
				if errParse != nil {
					// invalid cpu list is provided, set reservedSystemCPUs to empty, so it won't overwrite kubeReserved/systemReserved
					klog.InfoS("Invalid ReservedSystemCPUs", "systemReservedCPUs", s.ReservedSystemCPUs)
					return errParse
				}
				reservedList := reservedSystemCPUs.ToSlice()
				first := reservedList[0]
				last := reservedList[len(reservedList)-1]
				if first < 0 || last >= machineInfo.NumCores {
					// the specified cpuset is outside of the range of what the machine has
					klog.InfoS("Invalid cpuset specified by --reserved-cpus")
					return fmt.Errorf("Invalid cpuset %q specified by --reserved-cpus", s.ReservedSystemCPUs)
				}
			}
		} else {
			reservedSystemCPUs = cpuset.NewCPUSet()
		}
    
    if reservedSystemCPUs.Size() > 0 {
			// at cmd option valication phase it is tested either --system-reserved-cgroup or --kube-reserved-cgroup is specified, so overwrite should be ok
			klog.InfoS("Option --reserved-cpus is specified, it will overwrite the cpu setting in KubeReserved and SystemReserved", "kubeReservedCPUs", s.KubeReserved, "systemReservedCPUs", s.SystemReserved)
			if s.KubeReserved != nil {
				delete(s.KubeReserved, "cpu")
			}
			if s.SystemReserved == nil {
				s.SystemReserved = make(map[string]string)
			}
			s.SystemReserved["cpu"] = strconv.Itoa(reservedSystemCPUs.Size())
			klog.InfoS("After cpu setting is overwritten", "kubeReservedCPUs", s.KubeReserved, "systemReservedCPUs", s.SystemReserved)
		}

		kubeReserved, err := parseResourceList(s.KubeReserved)
		if err != nil {
			return err
		}
		systemReserved, err := parseResourceList(s.SystemReserved)
		if err != nil {
			return err
		}

    ···
  
		kubeDeps.ContainerManager, err = cm.NewContainerManager(
			kubeDeps.Mounter,
			kubeDeps.CAdvisorInterface,
			cm.NodeConfig{
				RuntimeCgroupsName:    s.RuntimeCgroups,
				SystemCgroupsName:     s.SystemCgroups,
				KubeletCgroupsName:    s.KubeletCgroups,
				ContainerRuntime:      s.ContainerRuntime,
				CgroupsPerQOS:         s.CgroupsPerQOS,
				CgroupRoot:            s.CgroupRoot,
				CgroupDriver:          s.CgroupDriver,
				KubeletRootDir:        s.RootDirectory,
				ProtectKernelDefaults: s.ProtectKernelDefaults,
				NodeAllocatableConfig: cm.NodeAllocatableConfig{
					KubeReservedCgroupName:   s.KubeReservedCgroup,
					SystemReservedCgroupName: s.SystemReservedCgroup,
					EnforceNodeAllocatable:   sets.NewString(s.EnforceNodeAllocatable...),
					KubeReserved:             kubeReserved,
					SystemReserved:           systemReserved,
					ReservedSystemCPUs:       reservedSystemCPUs,
					HardEvictionThresholds:   hardEvictionThresholds,
				},
				QOSReserved:                             *experimentalQOSReserved,
				ExperimentalCPUManagerPolicy:            s.CPUManagerPolicy,
				ExperimentalCPUManagerReconcilePeriod:   s.CPUManagerReconcilePeriod.Duration,
				ExperimentalMemoryManagerPolicy:         s.MemoryManagerPolicy,
				ExperimentalMemoryManagerReservedMemory: s.ReservedMemory,
				ExperimentalPodPidsLimit:                s.PodPidsLimit,
				EnforceCPULimits:                        s.CPUCFSQuota,
				CPUCFSQuotaPeriod:                       s.CPUCFSQuotaPeriod.Duration,
				ExperimentalTopologyManagerPolicy:       s.TopologyManagerPolicy,
				ExperimentalTopologyManagerScope:        s.TopologyManagerScope,
			},
			s.FailSwapOn,
			devicePluginEnabled,
			kubeDeps.Recorder)

		if err != nil {
			return err
		}
	}

  ···
}
```

和容器cpu相关的主要是以上的代码，流程主要是取到flag中定义的启动参数--reserved-cpus参数，并使用cpuset.Parse方法将其转换为cpuset类型，然后将ReservedSystemCPUs构建为一个切片数组，与通过cadvisor取到的machineinfo中的cpu字段进行一个对比，判断是否传入的reserved-cpus参数，有不属于该节点实际的cpu。校验通过后，如果ReservedSystemCPUs的值存在，则会覆盖掉另外的两个系统参数中KubeReserved和SystemReserved中关于cpu的相关数据。最后初始化cm.NewContainerManager



pkg/kubelet/cm/container_manager_linux.go

```go
func NewContainerManager(mountUtil mount.Interface, cadvisorInterface cadvisor.Interface, nodeConfig NodeConfig, failSwapOn bool, devicePluginEnabled bool, recorder record.EventRecorder) (ContainerManager, error) {
	...

	// Initialize CPU manager
	if utilfeature.DefaultFeatureGate.Enabled(kubefeatures.CPUManager) {
		cm.cpuManager, err = cpumanager.NewManager(
			nodeConfig.ExperimentalCPUManagerPolicy,
			nodeConfig.ExperimentalCPUManagerReconcilePeriod,
			machineInfo,
			nodeConfig.NodeAllocatableConfig.ReservedSystemCPUs,
			cm.GetNodeAllocatableReservation(),
			nodeConfig.KubeletRootDir,
			cm.topologyManager,
		)
		if err != nil {
			klog.ErrorS(err, "Failed to initialize cpu manager")
			return nil, err
		}
		cm.topologyManager.AddHintProvider(cm.cpuManager)
	}

  ...

	return cm, nil
}
```

NewContainerManager初始化containerManager，其中包含了多个实现的ContainerManager：cgroupManager，qosContainerManager，topologyManager，cpuManager，memoryManager等。主要看cpuManager相关的，判断cpuManager特性是否开启，如果特性开启的话，会调用**cpumanager.NewManager()**初始化一个cpuManager。具体代码如下：



pkg/kubelet/cm/cpumanager/cpu_manager.go

```go
// NewManager creates new cpu manager based on provided policy
func NewManager(cpuPolicyName string, reconcilePeriod time.Duration, machineInfo *cadvisorapi.MachineInfo, specificCPUs cpuset.CPUSet, nodeAllocatableReservation v1.ResourceList, stateFileDirectory string, affinity topologymanager.Store) (Manager, error) {
	var topo *topology.CPUTopology
	var policy Policy

	switch policyName(cpuPolicyName) {

	case PolicyNone:
		policy = NewNonePolicy()

	case PolicyStatic:
		var err error
		topo, err = topology.Discover(machineInfo)
		if err != nil {
			return nil, err
		}
		klog.InfoS("Detected CPU topology", "topology", topo)

		reservedCPUs, ok := nodeAllocatableReservation[v1.ResourceCPU]
		if !ok {
			// The static policy cannot initialize without this information.
			return nil, fmt.Errorf("[cpumanager] unable to determine reserved CPU resources for static policy")
		}
		if reservedCPUs.IsZero() {
			// The static policy requires this to be nonzero. Zero CPU reservation
			// would allow the shared pool to be completely exhausted. At that point
			// either we would violate our guarantee of exclusivity or need to evict
			// any pod that has at least one container that requires zero CPUs.
			// See the comments in policy_static.go for more details.
			return nil, fmt.Errorf("[cpumanager] the static policy requires systemreserved.cpu + kubereserved.cpu to be greater than zero")
		}

		// Take the ceiling of the reservation, since fractional CPUs cannot be
		// exclusively allocated.
		reservedCPUsFloat := float64(reservedCPUs.MilliValue()) / 1000
		numReservedCPUs := int(math.Ceil(reservedCPUsFloat))
		policy, err = NewStaticPolicy(topo, numReservedCPUs, specificCPUs, affinity)
		if err != nil {
			return nil, fmt.Errorf("new static policy error: %v", err)
		}

	default:
		return nil, fmt.Errorf("unknown policy: \"%s\"", cpuPolicyName)
	}

	manager := &manager{
		policy:                     policy,
		reconcilePeriod:            reconcilePeriod,
		topology:                   topo,
		nodeAllocatableReservation: nodeAllocatableReservation,
		stateFileDirectory:         stateFileDirectory,
	}
	manager.sourcesReady = &sourcesReadyStub{}
	return manager, nil
}
```



pkg/kubelet/cm/cpumanager/policy.go

```go
// Policy implements logic for pod container to CPU assignment.
type Policy interface {
	Name() string
	Start(s state.State) error
	// Allocate call is idempotent
	Allocate(s state.State, pod *v1.Pod, container *v1.Container) error
	// RemoveContainer call is idempotent
	RemoveContainer(s state.State, podUID string, containerName string) error
	// GetTopologyHints implements the topologymanager.HintProvider Interface
	// and is consulted to achieve NUMA aware resource alignment among this
	// and other resource controllers.
	GetTopologyHints(s state.State, pod *v1.Pod, container *v1.Container) map[string][]topologymanager.TopologyHint
	// GetPodTopologyHints implements the topologymanager.HintProvider Interface
	// and is consulted to achieve NUMA aware resource alignment per Pod
	// among this and other resource controllers.
	GetPodTopologyHints(s state.State, pod *v1.Pod) map[string][]topologymanager.TopologyHint
	// GetAllocatableCPUs returns the assignable (not allocated) CPUs
	GetAllocatableCPUs(m state.State) cpuset.CPUSet
}
```

NewManager会创建一个**topology.CPUTopology**，同时会声明一个Policy的接口，根据传入的不同Policy类型，调用具体的Policy实现，Policy需要实现具体的pod容器的cpu 分配方法。现在Kubernetes默认支持的policy有两种，具体参数为kubelet启动时传入的--cpu-manager-policy=static，默认不启用default的情况，Policy就是none。



pkg/kubelet/cm/cpumanager/policy_none.go

```go
/*
Copyright 2017 The Kubernetes Authors.

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

package cpumanager

import (
	"k8s.io/api/core/v1"
	"k8s.io/klog/v2"
	"k8s.io/kubernetes/pkg/kubelet/cm/cpumanager/state"
	"k8s.io/kubernetes/pkg/kubelet/cm/cpuset"
	"k8s.io/kubernetes/pkg/kubelet/cm/topologymanager"
)

type nonePolicy struct{}

var _ Policy = &nonePolicy{}

// PolicyNone name of none policy
const PolicyNone policyName = "none"

// NewNonePolicy returns a cpuset manager policy that does nothing
func NewNonePolicy() Policy {
	return &nonePolicy{}
}

func (p *nonePolicy) Name() string {
	return string(PolicyNone)
}

func (p *nonePolicy) Start(s state.State) error {
	klog.InfoS("None policy: Start")
	return nil
}

func (p *nonePolicy) Allocate(s state.State, pod *v1.Pod, container *v1.Container) error {
	return nil
}

func (p *nonePolicy) RemoveContainer(s state.State, podUID string, containerName string) error {
	return nil
}

func (p *nonePolicy) GetTopologyHints(s state.State, pod *v1.Pod, container *v1.Container) map[string][]topologymanager.TopologyHint {
	return nil
}

func (p *nonePolicy) GetPodTopologyHints(s state.State, pod *v1.Pod) map[string][]topologymanager.TopologyHint {
	return nil
}

// Assignable CPUs are the ones that can be exclusively allocated to pods that meet the exclusivity requirement
// (ie guaranteed QoS class and integral CPU request).
// Assignability of CPUs as a concept is only applicable in case of static policy i.e. scenarios where workloads
// CAN get exclusive access to core(s).
// Hence, we return empty set here: no cpus are assignable according to above definition with this policy.
func (p *nonePolicy) GetAllocatableCPUs(m state.State) cpuset.CPUSet {
	return cpuset.NewCPUSet()
}

```

默认None Policy没有具体实现Policy接口，仅仅返回空值，表示对cpu分配不做管理。

而Static Policy首先在创建CPUManager的时候就会进行校验，首先根据cadvisor取到的machineInfo（节点信息），根据**topology.Dicsovery(machineInfo)**可以生成具体的cpu拓扑信息,包含了节点cpu逻辑核数，物理核数和cpu socket数

![image-20220809155145496](https://github.com/JING21/K8S/raw/main/kubelet/cpumanager/topo.png)

pkg/kubelet/cm/cpumanager/topology/topology.go

```go
// CPUTopology contains details of node cpu, where :
// CPU  - logical CPU, cadvisor - thread
// Core - physical CPU, cadvisor - Core
// Socket - socket, cadvisor - Node
type CPUTopology struct {
	NumCPUs    int
	NumCores   int
	NumSockets int
	CPUDetails CPUDetails
}

// CPUDetails is a map from CPU ID to Core ID, Socket ID, and NUMA ID.
type CPUDetails map[int]CPUInfo

// CPUInfo contains the NUMA, socket, and core IDs associated with a CPU.
type CPUInfo struct {
	NUMANodeID int
	SocketID   int
	CoreID     int
}
```

接下来就会进行校验工作，判断reservedCPUS是否存在，启用Static CPU Policy必须开启reserved-CPUs的特性。在确保reservedCPUs不为0的情况下，向上取整计算出具体保留的numReservedCPUs。同时调用NewStaticPolicy()方法，初始化StaticPolicy。

pkg/kubelet/cm/cpumanager/policy_static.go

```go
// NewStaticPolicy returns a CPU manager policy that does not change CPU
// assignments for exclusively pinned guaranteed containers after the main
// container process starts.
func NewStaticPolicy(topology *topology.CPUTopology, numReservedCPUs int, reservedCPUs cpuset.CPUSet, affinity topologymanager.Store) (Policy, error) {
	allCPUs := topology.CPUDetails.CPUs()
	var reserved cpuset.CPUSet
	if reservedCPUs.Size() > 0 {
		reserved = reservedCPUs
	} else {
		// takeByTopology allocates CPUs associated with low-numbered cores from
		// allCPUs.
		//
		// For example: Given a system with 8 CPUs available and HT enabled,
		// if numReservedCPUs=2, then reserved={0,4}
		reserved, _ = takeByTopology(topology, allCPUs, numReservedCPUs)
	}

	if reserved.Size() != numReservedCPUs {
		err := fmt.Errorf("[cpumanager] unable to reserve the required amount of CPUs (size of %s did not equal %d)", reserved, numReservedCPUs)
		return nil, err
	}

	klog.InfoS("Reserved CPUs not available for exclusive assignment", "reservedSize", reserved.Size(), "reserved", reserved)

	return &staticPolicy{
		topology:    topology,
		reserved:    reserved,
		affinity:    affinity,
		cpusToReuse: make(map[string]cpuset.CPUSet),
	}, nil
}
```

NewStaticPolicy会校验传入的reservedCPUs，如果其为空，则会根据numReservedCPUs，在allCPUs中通过takeByTopology（）方法，随机保留下对应数量的cpu，比如说numReservedCPUs为2，可能会保留0，4两个核。

pkg/kubelet/cm/cpumanager/policy.go

```golang
type Policy interface {
	Name() string
	Start(s state.State) error
	// Allocate call is idempotent
	Allocate(s state.State, pod *v1.Pod, container *v1.Container) error
	// RemoveContainer call is idempotent
	RemoveContainer(s state.State, podUID string, containerName string) error
	// GetTopologyHints implements the topologymanager.HintProvider Interface
	// and is consulted to achieve NUMA aware resource alignment among this
	// and other resource controllers.
	GetTopologyHints(s state.State, pod *v1.Pod, container *v1.Container) map[string][]topologymanager.TopologyHint
	// GetPodTopologyHints implements the topologymanager.HintProvider Interface
	// and is consulted to achieve NUMA aware resource alignment per Pod
	// among this and other resource controllers.
	GetPodTopologyHints(s state.State, pod *v1.Pod) map[string][]topologymanager.TopologyHint
	// GetAllocatableCPUs returns the assignable (not allocated) CPUs
	GetAllocatableCPUs(m state.State) cpuset.CPUSet
}
```

cpu分配的具体policy需要实现以下具体的接口，包括了Name，Allocate(用于分配cpu给具体的pod的容器)，RemoveContainer， GetTopologyHints（根据pod的当前情况，获取cpu的topo关系），GetAllocatableCPUs（获取可用的cpuset），GetPodTopologyHints（实现了topologymanager.HintProvider的接口，生成NUMA亲和性相关性的map关系，用于cpu的分配）

pkg/kubelet/cm/cpumanager/policy_static.go

```golang
func (p *staticPolicy) validateState(s state.State) error {
	tmpAssignments := s.GetCPUAssignments()
	tmpDefaultCPUset := s.GetDefaultCPUSet()

	// Default cpuset cannot be empty when assignments exist
	if tmpDefaultCPUset.IsEmpty() {
		if len(tmpAssignments) != 0 {
			return fmt.Errorf("default cpuset cannot be empty")
		}
		// state is empty initialize
		allCPUs := p.topology.CPUDetails.CPUs()
		s.SetDefaultCPUSet(allCPUs)
		return nil
	}

	// State has already been initialized from file (is not empty)
	// 1. Check if the reserved cpuset is not part of default cpuset because:
	// - kube/system reserved have changed (increased) - may lead to some containers not being able to start
	// - user tampered with file
	if !p.reserved.Intersection(tmpDefaultCPUset).Equals(p.reserved) {
		return fmt.Errorf("not all reserved cpus: \"%s\" are present in defaultCpuSet: \"%s\"",
			p.reserved.String(), tmpDefaultCPUset.String())
	}

	// 2. Check if state for static policy is consistent
	for pod := range tmpAssignments {
		for container, cset := range tmpAssignments[pod] {
			// None of the cpu in DEFAULT cset should be in s.assignments
			if !tmpDefaultCPUset.Intersection(cset).IsEmpty() {
				return fmt.Errorf("pod: %s, container: %s cpuset: \"%s\" overlaps with default cpuset \"%s\"",
					pod, container, cset.String(), tmpDefaultCPUset.String())
			}
		}
	}

	// 3. It's possible that the set of available CPUs has changed since
	// the state was written. This can be due to for example
	// offlining a CPU when kubelet is not running. If this happens,
	// CPU manager will run into trouble when later it tries to
	// assign non-existent CPUs to containers. Validate that the
	// topology that was received during CPU manager startup matches with
	// the set of CPUs stored in the state.
	totalKnownCPUs := tmpDefaultCPUset.Clone()
	tmpCPUSets := []cpuset.CPUSet{}
	for pod := range tmpAssignments {
		for _, cset := range tmpAssignments[pod] {
			tmpCPUSets = append(tmpCPUSets, cset)
		}
	}
	totalKnownCPUs = totalKnownCPUs.UnionAll(tmpCPUSets)
	if !totalKnownCPUs.Equals(p.topology.CPUDetails.CPUs()) {
		return fmt.Errorf("current set of available CPUs \"%s\" doesn't match with CPUs in state \"%s\"",
			p.topology.CPUDetails.CPUs().String(), totalKnownCPUs.String())
	}

	return nil
}

// GetAllocatableCPUs returns the set of unassigned CPUs minus the reserved set.
func (p *staticPolicy) GetAllocatableCPUs(s state.State) cpuset.CPUSet {
	return s.GetDefaultCPUSet().Difference(p.reserved)
}

func (p *staticPolicy) updateCPUsToReuse(pod *v1.Pod, container *v1.Container, cset cpuset.CPUSet) {
	// If pod entries to m.cpusToReuse other than the current pod exist, delete them.
	for podUID := range p.cpusToReuse {
		if podUID != string(pod.UID) {
			delete(p.cpusToReuse, podUID)
		}
	}
	// If no cpuset exists for cpusToReuse by this pod yet, create one.
	if _, ok := p.cpusToReuse[string(pod.UID)]; !ok {
		p.cpusToReuse[string(pod.UID)] = cpuset.NewCPUSet()
	}
	// Check if the container is an init container.
	// If so, add its cpuset to the cpuset of reusable CPUs for any new allocations.
	for _, initContainer := range pod.Spec.InitContainers {
		if container.Name == initContainer.Name {
			p.cpusToReuse[string(pod.UID)] = p.cpusToReuse[string(pod.UID)].Union(cset)
			return
		}
	}
	// Otherwise it is an app container.
	// Remove its cpuset from the cpuset of reusable CPUs for any new allocations.
	p.cpusToReuse[string(pod.UID)] = p.cpusToReuse[string(pod.UID)].Difference(cset)
}

func (p *staticPolicy) Allocate(s state.State, pod *v1.Pod, container *v1.Container) error {
	if numCPUs := p.guaranteedCPUs(pod, container); numCPUs != 0 {
		klog.InfoS("Static policy: Allocate", "pod", klog.KObj(pod), "containerName", container.Name)
		// container belongs in an exclusively allocated pool

		if p.options.FullPhysicalCPUsOnly && ((numCPUs % p.topology.CPUsPerCore()) != 0) {
			// Since CPU Manager has been enabled requesting strict SMT alignment, it means a guaranteed pod can only be admitted
			// if the CPU requested is a multiple of the number of virtual cpus per physical cores.
			// In case CPU request is not a multiple of the number of virtual cpus per physical cores the Pod will be put
			// in Failed state, with SMTAlignmentError as reason. Since the allocation happens in terms of physical cores
			// and the scheduler is responsible for ensuring that the workload goes to a node that has enough CPUs,
			// the pod would be placed on a node where there are enough physical cores available to be allocated.
			// Just like the behaviour in case of static policy, takeByTopology will try to first allocate CPUs from the same socket
			// and only in case the request cannot be sattisfied on a single socket, CPU allocation is done for a workload to occupy all
			// CPUs on a physical core. Allocation of individual threads would never have to occur.
			return SMTAlignmentError{
				RequestedCPUs: numCPUs,
				CpusPerCore:   p.topology.CPUsPerCore(),
			}
		}
		if cpuset, ok := s.GetCPUSet(string(pod.UID), container.Name); ok {
			p.updateCPUsToReuse(pod, container, cpuset)
			klog.InfoS("Static policy: container already present in state, skipping", "pod", klog.KObj(pod), "containerName", container.Name)
			return nil
		}

		// Call Topology Manager to get the aligned socket affinity across all hint providers.
		hint := p.affinity.GetAffinity(string(pod.UID), container.Name)
		klog.InfoS("Topology Affinity", "pod", klog.KObj(pod), "containerName", container.Name, "affinity", hint)

		// Allocate CPUs according to the NUMA affinity contained in the hint.
		cpuset, err := p.allocateCPUs(s, numCPUs, hint.NUMANodeAffinity, p.cpusToReuse[string(pod.UID)])
		if err != nil {
			klog.ErrorS(err, "Unable to allocate CPUs", "pod", klog.KObj(pod), "containerName", container.Name, "numCPUs", numCPUs)
			return err
		}
		s.SetCPUSet(string(pod.UID), container.Name, cpuset)
		p.updateCPUsToReuse(pod, container, cpuset)

	}
	// container belongs in the shared pool (nothing to do; use default cpuset)
	return nil
}

// getAssignedCPUsOfSiblings returns assigned cpus of given container's siblings(all containers other than the given container) in the given pod `podUID`.
func getAssignedCPUsOfSiblings(s state.State, podUID string, containerName string) cpuset.CPUSet {
	assignments := s.GetCPUAssignments()
	cset := cpuset.NewCPUSet()
	for name, cpus := range assignments[podUID] {
		if containerName == name {
			continue
		}
		cset = cset.Union(cpus)
	}
	return cset
}

func (p *staticPolicy) RemoveContainer(s state.State, podUID string, containerName string) error {
	klog.InfoS("Static policy: RemoveContainer", "podUID", podUID, "containerName", containerName)
	cpusInUse := getAssignedCPUsOfSiblings(s, podUID, containerName)
	if toRelease, ok := s.GetCPUSet(podUID, containerName); ok {
		s.Delete(podUID, containerName)
		// Mutate the shared pool, adding released cpus.
		toRelease = toRelease.Difference(cpusInUse)
		s.SetDefaultCPUSet(s.GetDefaultCPUSet().Union(toRelease))
	}
	return nil
}

func (p *staticPolicy) allocateCPUs(s state.State, numCPUs int, numaAffinity bitmask.BitMask, reusableCPUs cpuset.CPUSet) (cpuset.CPUSet, error) {
	klog.InfoS("AllocateCPUs", "numCPUs", numCPUs, "socket", numaAffinity)

	allocatableCPUs := p.GetAllocatableCPUs(s).Union(reusableCPUs)

	// If there are aligned CPUs in numaAffinity, attempt to take those first.
	result := cpuset.NewCPUSet()
	if numaAffinity != nil {
		alignedCPUs := p.getAlignedCPUs(numaAffinity, allocatableCPUs)

		numAlignedToAlloc := alignedCPUs.Size()
		if numCPUs < numAlignedToAlloc {
			numAlignedToAlloc = numCPUs
		}

		alignedCPUs, err := p.takeByTopology(alignedCPUs, numAlignedToAlloc)
		if err != nil {
			return cpuset.NewCPUSet(), err
		}

		result = result.Union(alignedCPUs)
	}

	// Get any remaining CPUs from what's leftover after attempting to grab aligned ones.
	remainingCPUs, err := p.takeByTopology(allocatableCPUs.Difference(result), numCPUs-result.Size())
	if err != nil {
		return cpuset.NewCPUSet(), err
	}
	result = result.Union(remainingCPUs)

	// Remove allocated CPUs from the shared CPUSet.
	s.SetDefaultCPUSet(s.GetDefaultCPUSet().Difference(result))

	klog.InfoS("AllocateCPUs", "result", result)
	return result, nil
}

func (p *staticPolicy) guaranteedCPUs(pod *v1.Pod, container *v1.Container) int {
	if v1qos.GetPodQOS(pod) != v1.PodQOSGuaranteed {
		return 0
	}
	cpuQuantity := container.Resources.Requests[v1.ResourceCPU]
	if cpuQuantity.Value()*1000 != cpuQuantity.MilliValue() {
		return 0
	}
	// Safe downcast to do for all systems with < 2.1 billion CPUs.
	// Per the language spec, `int` is guaranteed to be at least 32 bits wide.
	// https://golang.org/ref/spec#Numeric_types
	return int(cpuQuantity.Value())
}

func (p *staticPolicy) podGuaranteedCPUs(pod *v1.Pod) int {
	// The maximum of requested CPUs by init containers.
	requestedByInitContainers := 0
	for _, container := range pod.Spec.InitContainers {
		if _, ok := container.Resources.Requests[v1.ResourceCPU]; !ok {
			continue
		}
		requestedCPU := p.guaranteedCPUs(pod, &container)
		if requestedCPU > requestedByInitContainers {
			requestedByInitContainers = requestedCPU
		}
	}
	// The sum of requested CPUs by app containers.
	requestedByAppContainers := 0
	for _, container := range pod.Spec.Containers {
		if _, ok := container.Resources.Requests[v1.ResourceCPU]; !ok {
			continue
		}
		requestedByAppContainers += p.guaranteedCPUs(pod, &container)
	}

	if requestedByInitContainers > requestedByAppContainers {
		return requestedByInitContainers
	}
	return requestedByAppContainers
}

func (p *staticPolicy) takeByTopology(availableCPUs cpuset.CPUSet, numCPUs int) (cpuset.CPUSet, error) {
	if p.options.DistributeCPUsAcrossNUMA {
		cpuGroupSize := 1
		if p.options.FullPhysicalCPUsOnly {
			cpuGroupSize = p.topology.CPUsPerCore()
		}
		return takeByTopologyNUMADistributed(p.topology, availableCPUs, numCPUs, cpuGroupSize)
	}
	return takeByTopologyNUMAPacked(p.topology, availableCPUs, numCPUs)
}

func (p *staticPolicy) GetTopologyHints(s state.State, pod *v1.Pod, container *v1.Container) map[string][]topologymanager.TopologyHint {
	// Get a count of how many guaranteed CPUs have been requested.
	requested := p.guaranteedCPUs(pod, container)

	// Number of required CPUs is not an integer or a container is not part of the Guaranteed QoS class.
	// It will be treated by the TopologyManager as having no preference and cause it to ignore this
	// resource when considering pod alignment.
	// In terms of hints, this is equal to: TopologyHints[NUMANodeAffinity: nil, Preferred: true].
	if requested == 0 {
		return nil
	}

	// Short circuit to regenerate the same hints if there are already
	// guaranteed CPUs allocated to the Container. This might happen after a
	// kubelet restart, for example.
	if allocated, exists := s.GetCPUSet(string(pod.UID), container.Name); exists {
		if allocated.Size() != requested {
			klog.ErrorS(nil, "CPUs already allocated to container with different number than request", "pod", klog.KObj(pod), "containerName", container.Name, "requestedSize", requested, "allocatedSize", allocated.Size())
			// An empty list of hints will be treated as a preference that cannot be satisfied.
			// In definition of hints this is equal to: TopologyHint[NUMANodeAffinity: nil, Preferred: false].
			// For all but the best-effort policy, the Topology Manager will throw a pod-admission error.
			return map[string][]topologymanager.TopologyHint{
				string(v1.ResourceCPU): {},
			}
		}
		klog.InfoS("Regenerating TopologyHints for CPUs already allocated", "pod", klog.KObj(pod), "containerName", container.Name)
		return map[string][]topologymanager.TopologyHint{
			string(v1.ResourceCPU): p.generateCPUTopologyHints(allocated, cpuset.CPUSet{}, requested),
		}
	}

	// Get a list of available CPUs.
	available := p.GetAllocatableCPUs(s)

	// Get a list of reusable CPUs (e.g. CPUs reused from initContainers).
	// It should be an empty CPUSet for a newly created pod.
	reusable := p.cpusToReuse[string(pod.UID)]

	// Generate hints.
	cpuHints := p.generateCPUTopologyHints(available, reusable, requested)
	klog.InfoS("TopologyHints generated", "pod", klog.KObj(pod), "containerName", container.Name, "cpuHints", cpuHints)

	return map[string][]topologymanager.TopologyHint{
		string(v1.ResourceCPU): cpuHints,
	}
}

func (p *staticPolicy) GetPodTopologyHints(s state.State, pod *v1.Pod) map[string][]topologymanager.TopologyHint {
	// Get a count of how many guaranteed CPUs have been requested by Pod.
	requested := p.podGuaranteedCPUs(pod)

	// Number of required CPUs is not an integer or a pod is not part of the Guaranteed QoS class.
	// It will be treated by the TopologyManager as having no preference and cause it to ignore this
	// resource when considering pod alignment.
	// In terms of hints, this is equal to: TopologyHints[NUMANodeAffinity: nil, Preferred: true].
	if requested == 0 {
		return nil
	}

	assignedCPUs := cpuset.NewCPUSet()
	for _, container := range append(pod.Spec.InitContainers, pod.Spec.Containers...) {
		requestedByContainer := p.guaranteedCPUs(pod, &container)
		// Short circuit to regenerate the same hints if there are already
		// guaranteed CPUs allocated to the Container. This might happen after a
		// kubelet restart, for example.
		if allocated, exists := s.GetCPUSet(string(pod.UID), container.Name); exists {
			if allocated.Size() != requestedByContainer {
				klog.ErrorS(nil, "CPUs already allocated to container with different number than request", "pod", klog.KObj(pod), "containerName", container.Name, "allocatedSize", requested, "requestedByContainer", requestedByContainer, "allocatedSize", allocated.Size())
				// An empty list of hints will be treated as a preference that cannot be satisfied.
				// In definition of hints this is equal to: TopologyHint[NUMANodeAffinity: nil, Preferred: false].
				// For all but the best-effort policy, the Topology Manager will throw a pod-admission error.
				return map[string][]topologymanager.TopologyHint{
					string(v1.ResourceCPU): {},
				}
			}
			// A set of CPUs already assigned to containers in this pod
			assignedCPUs = assignedCPUs.Union(allocated)
		}
	}
	if assignedCPUs.Size() == requested {
		klog.InfoS("Regenerating TopologyHints for CPUs already allocated", "pod", klog.KObj(pod))
		return map[string][]topologymanager.TopologyHint{
			string(v1.ResourceCPU): p.generateCPUTopologyHints(assignedCPUs, cpuset.CPUSet{}, requested),
		}
	}

	// Get a list of available CPUs.
	available := p.GetAllocatableCPUs(s)

	// Get a list of reusable CPUs (e.g. CPUs reused from initContainers).
	// It should be an empty CPUSet for a newly created pod.
	reusable := p.cpusToReuse[string(pod.UID)]

	// Ensure any CPUs already assigned to containers in this pod are included as part of the hint generation.
	reusable = reusable.Union(assignedCPUs)

	// Generate hints.
	cpuHints := p.generateCPUTopologyHints(available, reusable, requested)
	klog.InfoS("TopologyHints generated", "pod", klog.KObj(pod), "cpuHints", cpuHints)

	return map[string][]topologymanager.TopologyHint{
		string(v1.ResourceCPU): cpuHints,
	}
}

// generateCPUtopologyHints generates a set of TopologyHints given the set of
// available CPUs and the number of CPUs being requested.
//
// It follows the convention of marking all hints that have the same number of
// bits set as the narrowest matching NUMANodeAffinity with 'Preferred: true', and
// marking all others with 'Preferred: false'.
func (p *staticPolicy) generateCPUTopologyHints(availableCPUs cpuset.CPUSet, reusableCPUs cpuset.CPUSet, request int) []topologymanager.TopologyHint {
	// Initialize minAffinitySize to include all NUMA Nodes.
	minAffinitySize := p.topology.CPUDetails.NUMANodes().Size()

	// Iterate through all combinations of numa nodes bitmask and build hints from them.
	hints := []topologymanager.TopologyHint{}
	bitmask.IterateBitMasks(p.topology.CPUDetails.NUMANodes().ToSlice(), func(mask bitmask.BitMask) {
		// First, update minAffinitySize for the current request size.
		cpusInMask := p.topology.CPUDetails.CPUsInNUMANodes(mask.GetBits()...).Size()
		if cpusInMask >= request && mask.Count() < minAffinitySize {
			minAffinitySize = mask.Count()
		}

		// Then check to see if we have enough CPUs available on the current
		// numa node bitmask to satisfy the CPU request.
		numMatching := 0
		for _, c := range reusableCPUs.ToSlice() {
			// Disregard this mask if its NUMANode isn't part of it.
			if !mask.IsSet(p.topology.CPUDetails[c].NUMANodeID) {
				return
			}
			numMatching++
		}

		// Finally, check to see if enough available CPUs remain on the current
		// NUMA node combination to satisfy the CPU request.
		for _, c := range availableCPUs.ToSlice() {
			if mask.IsSet(p.topology.CPUDetails[c].NUMANodeID) {
				numMatching++
			}
		}

		// If they don't, then move onto the next combination.
		if numMatching < request {
			return
		}

		// Otherwise, create a new hint from the numa node bitmask and add it to the
		// list of hints.  We set all hint preferences to 'false' on the first
		// pass through.
		hints = append(hints, topologymanager.TopologyHint{
			NUMANodeAffinity: mask,
			Preferred:        false,
		})
	})

	// Loop back through all hints and update the 'Preferred' field based on
	// counting the number of bits sets in the affinity mask and comparing it
	// to the minAffinitySize. Only those with an equal number of bits set (and
	// with a minimal set of numa nodes) will be considered preferred.
	for i := range hints {
		if p.options.AlignBySocket && p.isHintSocketAligned(hints[i], minAffinitySize) {
			hints[i].Preferred = true
			continue
		}
		if hints[i].NUMANodeAffinity.Count() == minAffinitySize {
			hints[i].Preferred = true
		}
	}

	return hints
}

// isHintSocketAligned function return true if numa nodes in hint are socket aligned.
func (p *staticPolicy) isHintSocketAligned(hint topologymanager.TopologyHint, minAffinitySize int) bool {
	numaNodesBitMask := hint.NUMANodeAffinity.GetBits()
	numaNodesPerSocket := p.topology.NumNUMANodes / p.topology.NumSockets
	if numaNodesPerSocket == 0 {
		return false
	}
	// minSockets refers to minimum number of socket required to satify allocation.
	// A hint is considered socket aligned if sockets across which numa nodes span is equal to minSockets
	minSockets := (minAffinitySize + numaNodesPerSocket - 1) / numaNodesPerSocket
	return p.topology.CPUDetails.SocketsInNUMANodes(numaNodesBitMask...).Size() == minSockets
}

// getAlignedCPUs return set of aligned CPUs based on numa affinity mask and configured policy options.
func (p *staticPolicy) getAlignedCPUs(numaAffinity bitmask.BitMask, allocatableCPUs cpuset.CPUSet) cpuset.CPUSet {
	alignedCPUs := cpuset.NewCPUSet()
	numaBits := numaAffinity.GetBits()

	// If align-by-socket policy option is enabled, NUMA based hint is expanded to
	// socket aligned hint. It will ensure that first socket aligned available CPUs are
	// allocated before we try to find CPUs across socket to satisfy allocation request.
	if p.options.AlignBySocket {
		socketBits := p.topology.CPUDetails.SocketsInNUMANodes(numaBits...).ToSliceNoSort()
		for _, socketID := range socketBits {
			alignedCPUs = alignedCPUs.Union(allocatableCPUs.Intersection(p.topology.CPUDetails.CPUsInSockets(socketID)))
		}
		return alignedCPUs
	}

	for _, numaNodeID := range numaBits {
		alignedCPUs = alignedCPUs.Union(allocatableCPUs.Intersection(p.topology.CPUDetails.CPUsInNUMANodes(numaNodeID)))
	}

	return alignedCPUs
}

```



pkg/kubelet/cm/cpumanager/policy_static.go

```go
// ContainerCPUAssignments type used in cpu manager state
type ContainerCPUAssignments map[string]map[string]cpuset.CPUSet

// Clone returns a copy of ContainerCPUAssignments
func (as ContainerCPUAssignments) Clone() ContainerCPUAssignments {
	ret := make(ContainerCPUAssignments)
	for pod := range as {
		ret[pod] = make(map[string]cpuset.CPUSet)
		for container, cset := range as[pod] {
			ret[pod][container] = cset
		}
	}
	return ret
}

// Reader interface used to read current cpu/pod assignment state
type Reader interface {
	GetCPUSet(podUID string, containerName string) (cpuset.CPUSet, bool)
	GetDefaultCPUSet() cpuset.CPUSet
	GetCPUSetOrDefault(podUID string, containerName string) cpuset.CPUSet
	GetCPUAssignments() ContainerCPUAssignments
}

type writer interface {
	SetCPUSet(podUID string, containerName string, cpuset cpuset.CPUSet)
	SetDefaultCPUSet(cpuset cpuset.CPUSet)
	SetCPUAssignments(ContainerCPUAssignments)
	Delete(podUID string, containerName string)
	ClearState()
}

// State interface provides methods for tracking and setting cpu/pod assignment
type State interface {
	Reader
	writer
}

```

以上是static-policy的策略的具体实现，分析一下其中主要的具体方法：

- **validateState(s state.State) **方法:  

  state数据结构可以理解为cpu-policy的缓存，缓存了具体的cpu绑定的情况和现存的剩余cpu情况，state接口分别包含了**Reader**和**Writer**两个接口，需要实现**GetCPUSet**和**SetCPUSet**等方法。

  首先会通过缓存方法**s.GetCPUAssignments**获取当前cpu的分配情况，通过**s.GetDefaultCPUSet**获取当前default的cpuset（pod在去除reserved的情况下，再去除单独绑核的cpu后剩余的cpu集合）

  当tmpDefaultCPUset为空时，即首次初始化时，会通过**topology.CPUDetails.CPUs()**方法获取节点所有的CPU，并且存入缓存设置为DefaultCPUSet

  当tmpDefaultCPUset缓存不为空时，会校验两种情况，

  1.校验reserved的cpuset不在default中

  2.校验已分配的cpu不在剩余的defaultcpu池中

  3.校验实际可用的CPUs和缓存对比是否发生变化（比如说下线了一个cpu，同时kubelet没有在正常工作），获取到缓存中的DefaultCPUs和计算出已分配的cpu合计，使用**unionall**方法和新使用**topology.CPUDetails.CPUs()**获取的cpu进行对比，是否相同

- **GetAllocatableCPUs (s state.State)**方法:

  从缓存中获取default-cpus除去reserved-cpus以外的cpus，设置为可分配cpu

- **Allocate(s state.State, pod *v1.Pod, container *v1.Container)**方法：

  核心方法用于分配CPU,首先会分为两种情况判断pod的类型：

  1.首先使用p.guaranteedCPUs判断是否是Guaranteed类型的pod，并且获取所需要的cpu核数，（需要是整数）当pod是Guaranteed类型时，表明pod有独占CPU核的需求。然后从缓存中获取信息，查看该pod是否已经创建，并分配了cpu，如果存在，仅需要**p.updateCPUToReuse()**进行更新。如果是新创建的pod，需要绑核操作，使用**p.affinity.GetAffinity()**方法获取亲和性拓扑关系，然后根据numa亲和性进行分配使用**p.allocateCPUs()**方法，最后使用**setCPUSet**更新缓存，最后调用**p.updateCPUToReuse()**更新实际的cpu

  2.如果不是Guaranteed类型的pod，则会不作处理，直接使用default cpuset

- **allocateCPUs(s state.State, numCPUs int, numaAffinity bitmask.BitMask, reusableCPUs cpuset.CPUSet)**方法：

  首先调用**p.GetAllocatableCPUs(s).Union(reusableCPUs)**获取可供分配的cpu，如果根据numaAffinity有匹配一致的cpu，优先使用这些cpu。分为两种情况：

  1.当存在numaAffinity时，根据具体的cpuID和numaNode的对应关系得出具体的cpu

  2.当不存在numaAffinity时，直接根据**takeByTopology**方法获取剩余的cpus

  之后将亲和性策略计算出的结果，与默认的defaultcpuset区分开，并且返回亲和性结果的cpuset

- **GetTopologyHints(s state.State, pod *v1.Pod, container *v1.Container)**方法：

  首先确定pod属于Guaranteed Pod的类型，获取所需cpu核数**request**，然后从缓存中对比，该pod是否已分配cpu，如果已分配的cpu和计算出所需核数**request**不同，则返回拓扑亲和性结果为空，如果核数和分配数相同，则调用**p.generateCPUTopologyHints**方法计算亲和性。如果该pod是初次分配cpu，不存在缓存的情况，则通过扣除缓存**p.GetAllocatableCPUs(s)**计算出可用的cpu **available**，再通过**p.generateCPUTopologyHints**计算出亲和性

- **GetPodTopologyHints(s state.State, pod *v1.Pod)**方法：

  同样首先会确认pod是否属于Guaranteed类型，然后遍历pod中所有的容器，包括initContainer,通过方法**assignedCPUs.Union(allocated)**计算出所有已分配的container所需的cpu核数：**assignedCPUs** 。同**GetTopologyHints(s state.State, pod *v1.Pod, container *v1.Container)**方法一样，获取所需cpu核数**request**，然后从缓存中对比，该pod是否已分配cpu，如果已分配的cpu和计算出所需核数**request**不同，则返回拓扑亲和性结果为空，如果核数和分配数相同，则调用**p.generateCPUTopologyHints**方法计算亲和性。



pkg/kubelet/kubelet.go

```go
func (kl *Kubelet) initializeRuntimeDependentModules() {
	if err := kl.cadvisor.Start(); err != nil {
		// Fail kubelet and rely on the babysitter to retry starting kubelet.
		klog.ErrorS(err, "Failed to start cAdvisor")
		os.Exit(1)
	}

  .........
	// containerManager must start after cAdvisor because it needs filesystem capacity information
	if err := kl.containerManager.Start(node, kl.GetActivePods, kl.sourcesReady, kl.statusManager, kl.runtimeService); err != nil {
		// Fail kubelet and rely on the babysitter to retry starting kubelet.
		klog.ErrorS(err, "Failed to start ContainerManager")
		os.Exit(1)
	}

	.........
	}
}
```

pkg/kubelet/cm/container_manager_linux.go

````go
func (cm *containerManagerImpl) Start(node *v1.Node,
	runtimeService internalapi.RuntimeService) error {
······
	// Initialize CPU manager
	if utilfeature.DefaultFeatureGate.Enabled(kubefeatures.CPUManager) {
		containerMap, err := buildContainerMapFromRuntime(runtimeService)
		if err != nil {
			return fmt.Errorf("failed to build map of initial containers from runtime: %v", err)
		}
		err = cm.cpuManager.Start(cpumanager.ActivePodsFunc(activePods), sourcesReady, podStatusProvider, runtimeService, containerMap)
		if err != nil {
			return fmt.Errorf("start cpu manager error: %v", err)
		}
	}

·········
	return nil
}
````

具体调用逻辑：在Kubelet方法**initializeRuntimeDependentModule**s中调用ContainerManager的**Start**方法，具体实现是container_manager_linux.go的**Start**方法:与cpu相关的就是**cm.cpuManager.Start**



​		
