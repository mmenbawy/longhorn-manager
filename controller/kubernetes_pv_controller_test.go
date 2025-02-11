package controller

import (
	"fmt"
	"sort"

	"github.com/sirupsen/logrus"

	"github.com/longhorn/longhorn-manager/datastore"
	"github.com/longhorn/longhorn-manager/types"

	apiv1 "k8s.io/api/core/v1"
	storagev1 "k8s.io/api/storage/v1beta1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/record"
	"k8s.io/kubernetes/pkg/controller"

	longhorn "github.com/longhorn/longhorn-manager/k8s/pkg/apis/longhorn/v1beta1"
	lhfake "github.com/longhorn/longhorn-manager/k8s/pkg/client/clientset/versioned/fake"
	lhinformerfactory "github.com/longhorn/longhorn-manager/k8s/pkg/client/informers/externalversions"

	. "gopkg.in/check.v1"
)

const (
	TestWorkloadName       = "test-statefulset"
	TestWorkloadKind       = "StatefulSet"
	TestWorkloadReplicaSet = "test-replicaset"
	TestReplicaSetKind     = "ReplicaSet"

	TestStatusDeleted = "Deleted"
)

var (
	storageClassName = "longhorn"
	pvcVolumeMode    = apiv1.PersistentVolumeFilesystem
)

type KubernetesTestCase struct {
	volume *longhorn.Volume
	pv     *apiv1.PersistentVolume
	pvc    *apiv1.PersistentVolumeClaim
	pods   []*apiv1.Pod

	expectVolume *longhorn.Volume
}

type DisasterRecoveryTestCase struct {
	volume *longhorn.Volume
	pv     *apiv1.PersistentVolume
	pvc    *apiv1.PersistentVolumeClaim
	pods   []*apiv1.Pod
	node   *longhorn.Node
	va     *storagev1.VolumeAttachment

	vaShouldExist bool
}

func generateKubernetesTestCaseTemplate() *KubernetesTestCase {
	volume := newVolume(TestVolumeName, 2)
	pv := newPV()
	pvc := newPVC()
	pods := []*apiv1.Pod{newPodWithPVC(TestPod1)}

	return &KubernetesTestCase{
		volume: volume,
		pv:     pv,
		pvc:    pvc,
		pods:   pods,

		expectVolume: nil,
	}
}

func generateDisasterRecoveryTestCaseTemplate() *DisasterRecoveryTestCase {
	volume := newVolume(TestVolumeName, 2)
	pv := newPV()
	pvc := newPVC()
	pods := []*apiv1.Pod{newPodWithPVC(TestPod1)}
	va := newVA(TestVAName, TestNode1, TestPVName)

	return &DisasterRecoveryTestCase{
		volume: volume,
		pv:     pv,
		pvc:    pvc,
		pods:   pods,
		node:   nil,
		va:     va,
	}
}

func (tc *KubernetesTestCase) copyCurrentToExpect() {
	tc.expectVolume = tc.volume.DeepCopy()
}

func newPV() *apiv1.PersistentVolume {
	return &apiv1.PersistentVolume{
		ObjectMeta: metav1.ObjectMeta{
			Name: TestPVName,
		},
		Spec: apiv1.PersistentVolumeSpec{
			Capacity: apiv1.ResourceList{
				apiv1.ResourceStorage: *resource.NewQuantity(1, resource.BinarySI),
			},
			VolumeMode: &pvcVolumeMode,
			PersistentVolumeSource: apiv1.PersistentVolumeSource{
				CSI: &apiv1.CSIPersistentVolumeSource{
					Driver: types.LonghornDriverName,
					FSType: "ext4",
					VolumeAttributes: map[string]string{
						"numberOfReplicas":    "3",
						"staleReplicaTimeout": "30",
					},
					VolumeHandle: TestVolumeName,
				},
			},
			ClaimRef: &apiv1.ObjectReference{
				Name:      TestPVCName,
				Namespace: TestNamespace,
			},
		},
		Status: apiv1.PersistentVolumeStatus{
			Phase: apiv1.VolumeBound,
		},
	}
}

func newPVC() *apiv1.PersistentVolumeClaim {
	return &apiv1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name: TestPVCName,
		},
		Spec: apiv1.PersistentVolumeClaimSpec{
			AccessModes: []apiv1.PersistentVolumeAccessMode{
				apiv1.ReadWriteOnce,
			},
			Resources: apiv1.ResourceRequirements{
				Requests: apiv1.ResourceList{
					apiv1.ResourceStorage: *resource.NewQuantity(1, resource.BinarySI),
				},
			},
			VolumeName: TestPVName,
		},
	}
}

func newPodWithPVC(podName string) *apiv1.Pod {
	return &apiv1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      podName,
			Namespace: TestNamespace,
			OwnerReferences: []metav1.OwnerReference{
				{
					Kind: TestWorkloadKind,
					Name: TestWorkloadName,
				},
			},
		},
		Spec: apiv1.PodSpec{
			Containers: []apiv1.Container{
				{
					Name:            podName,
					Image:           "nginx:stable-alpine",
					ImagePullPolicy: apiv1.PullIfNotPresent,
					VolumeMounts: []apiv1.VolumeMount{
						{
							Name:      "vol",
							MountPath: "/data",
						},
					},
					Ports: []apiv1.ContainerPort{
						{
							ContainerPort: 80,
						},
					},
				},
			},
			Volumes: []apiv1.Volume{
				{
					Name: "vol",
					VolumeSource: apiv1.VolumeSource{
						PersistentVolumeClaim: &apiv1.PersistentVolumeClaimVolumeSource{
							ClaimName: TestPVCName,
						},
					},
				},
			},
		},
		Status: apiv1.PodStatus{
			Phase: apiv1.PodRunning,
		},
	}
}

func newVA(vaName, nodeName, pvName string) *storagev1.VolumeAttachment {
	return &storagev1.VolumeAttachment{
		ObjectMeta: metav1.ObjectMeta{
			Name:              vaName,
			CreationTimestamp: metav1.Now(),
		},
		Spec: storagev1.VolumeAttachmentSpec{
			Attacher: types.LonghornDriverName,
			NodeName: nodeName,
			Source: storagev1.VolumeAttachmentSource{
				PersistentVolumeName: &pvName,
			},
		},
		Status: storagev1.VolumeAttachmentStatus{
			Attached: true,
		},
	}
}

func newTestKubernetesPVController(lhInformerFactory lhinformerfactory.SharedInformerFactory, kubeInformerFactory informers.SharedInformerFactory,
	lhClient *lhfake.Clientset, kubeClient *fake.Clientset) *KubernetesPVController {

	volumeInformer := lhInformerFactory.Longhorn().V1beta1().Volumes()
	engineInformer := lhInformerFactory.Longhorn().V1beta1().Engines()
	replicaInformer := lhInformerFactory.Longhorn().V1beta1().Replicas()
	engineImageInformer := lhInformerFactory.Longhorn().V1beta1().EngineImages()
	nodeInformer := lhInformerFactory.Longhorn().V1beta1().Nodes()
	settingInformer := lhInformerFactory.Longhorn().V1beta1().Settings()
	imInformer := lhInformerFactory.Longhorn().V1beta1().InstanceManagers()
	shareManagerInformer := lhInformerFactory.Longhorn().V1beta1().ShareManagers()
	backingImageInformer := lhInformerFactory.Longhorn().V1beta1().BackingImages()
	backingImageManagerInformer := lhInformerFactory.Longhorn().V1beta1().BackingImageManagers()

	podInformer := kubeInformerFactory.Core().V1().Pods()
	persistentVolumeInformer := kubeInformerFactory.Core().V1().PersistentVolumes()
	persistentVolumeClaimInformer := kubeInformerFactory.Core().V1().PersistentVolumeClaims()
	configMapInformer := kubeInformerFactory.Core().V1().ConfigMaps()
	secretInformer := kubeInformerFactory.Core().V1().Secrets()
	cronJobInformer := kubeInformerFactory.Batch().V1beta1().CronJobs()
	daemonSetInformer := kubeInformerFactory.Apps().V1().DaemonSets()
	deploymentInformer := kubeInformerFactory.Apps().V1().Deployments()
	volumeAttachmentInformer := kubeInformerFactory.Storage().V1beta1().VolumeAttachments()
	kubeNodeInformer := kubeInformerFactory.Core().V1().Nodes()
	priorityClassInformer := kubeInformerFactory.Scheduling().V1().PriorityClasses()
	csiDriverInformer := kubeInformerFactory.Storage().V1beta1().CSIDrivers()
	storageclassInformer := kubeInformerFactory.Storage().V1().StorageClasses()
	pdbInformer := kubeInformerFactory.Policy().V1beta1().PodDisruptionBudgets()
	serviceInformer := kubeInformerFactory.Core().V1().Services()

	ds := datastore.NewDataStore(
		volumeInformer, engineInformer, replicaInformer,
		engineImageInformer, nodeInformer, settingInformer,
		imInformer, shareManagerInformer,
		backingImageInformer, backingImageManagerInformer,
		lhClient,
		podInformer, cronJobInformer, daemonSetInformer,
		deploymentInformer, persistentVolumeInformer, persistentVolumeClaimInformer,
		configMapInformer, secretInformer, kubeNodeInformer, priorityClassInformer,
		csiDriverInformer, storageclassInformer,
		pdbInformer,
		serviceInformer,
		kubeClient, TestNamespace)

	logger := logrus.StandardLogger()
	kc := NewKubernetesPVController(logger,
		ds, scheme.Scheme,
		volumeInformer, persistentVolumeInformer, persistentVolumeClaimInformer, podInformer, volumeAttachmentInformer,
		kubeClient, TestNode1)

	fakeRecorder := record.NewFakeRecorder(100)
	kc.eventRecorder = fakeRecorder

	kc.pvStoreSynced = alwaysReady
	kc.pvcStoreSynced = alwaysReady
	kc.pStoreSynced = alwaysReady

	kc.nowHandler = getTestNow

	return kc
}

func (s *TestSuite) TestSyncKubernetesStatus(c *C) {
	deleteTime := metav1.Now()
	var workloads []types.WorkloadStatus
	var tc *KubernetesTestCase
	testCases := map[string]*KubernetesTestCase{}

	// pod + pvc + pv + workload set
	tc = generateKubernetesTestCaseTemplate()
	tc.copyCurrentToExpect()
	tc.pv.Status.Phase = apiv1.VolumeBound
	tc.pods = append(tc.pods, newPodWithPVC(TestPod2))
	workloads = []types.WorkloadStatus{}
	for _, p := range tc.pods {
		ws := types.WorkloadStatus{
			PodName:      p.Name,
			PodStatus:    string(p.Status.Phase),
			WorkloadName: TestWorkloadName,
			WorkloadType: TestWorkloadKind,
		}
		workloads = append(workloads, ws)
	}
	tc.expectVolume.Status.KubernetesStatus = types.KubernetesStatus{
		PVName:          TestPVName,
		PVStatus:        string(apiv1.VolumeBound),
		Namespace:       TestNamespace,
		PVCName:         TestPVCName,
		WorkloadsStatus: workloads,
	}
	testCases["all set"] = tc

	// volume unset
	tc = generateKubernetesTestCaseTemplate()
	tc.copyCurrentToExpect()
	tc.volume = nil
	tc.pv.Status.Phase = apiv1.VolumeBound
	workloads = []types.WorkloadStatus{}
	for _, p := range tc.pods {
		ws := types.WorkloadStatus{
			PodName:      p.Name,
			PodStatus:    string(p.Status.Phase),
			WorkloadName: TestWorkloadName,
			WorkloadType: TestWorkloadKind,
		}
		workloads = append(workloads, ws)
	}
	tc.expectVolume.Status.KubernetesStatus = types.KubernetesStatus{}
	testCases["volume unset"] = tc

	// pv unset
	tc = generateKubernetesTestCaseTemplate()
	tc.copyCurrentToExpect()
	tc.pv = nil
	workloads = []types.WorkloadStatus{}
	for _, p := range tc.pods {
		ws := types.WorkloadStatus{
			PodName:      p.Name,
			PodStatus:    string(p.Status.Phase),
			WorkloadName: TestWorkloadName,
			WorkloadType: TestWorkloadKind,
		}
		workloads = append(workloads, ws)
	}
	tc.expectVolume.Status.KubernetesStatus = types.KubernetesStatus{}
	testCases["pv unset"] = tc

	// pvc unset
	tc = generateKubernetesTestCaseTemplate()
	tc.copyCurrentToExpect()
	tc.pv.Status.Phase = apiv1.VolumeAvailable
	tc.pv.Spec.ClaimRef = nil
	tc.pvc = nil
	tc.expectVolume.Status.KubernetesStatus = types.KubernetesStatus{
		PVName:   TestPVName,
		PVStatus: string(apiv1.VolumeAvailable),
	}
	testCases["pvc unset"] = tc

	// pod unset
	tc = generateKubernetesTestCaseTemplate()
	tc.copyCurrentToExpect()
	tc.pv.Status.Phase = apiv1.VolumeBound
	tc.pods = nil
	tc.expectVolume.Status.KubernetesStatus = types.KubernetesStatus{
		PVName:    TestPVName,
		PVStatus:  string(apiv1.VolumeBound),
		Namespace: TestNamespace,
		PVCName:   TestPVCName,
	}
	testCases["pod unset"] = tc

	// workload unset
	tc = generateKubernetesTestCaseTemplate()
	tc.copyCurrentToExpect()
	tc.pv.Status.Phase = apiv1.VolumeBound
	tc.pods = append(tc.pods, newPodWithPVC(TestPod2))
	workloads = []types.WorkloadStatus{}
	for _, p := range tc.pods {
		p.ObjectMeta.OwnerReferences = nil
		ws := types.WorkloadStatus{
			PodName:   p.Name,
			PodStatus: string(p.Status.Phase),
		}
		workloads = append(workloads, ws)
	}
	tc.expectVolume.Status.KubernetesStatus = types.KubernetesStatus{
		PVName:          TestPVName,
		PVStatus:        string(apiv1.VolumeBound),
		Namespace:       TestNamespace,
		PVCName:         TestPVCName,
		WorkloadsStatus: workloads,
	}
	testCases["workload unset"] = tc

	// pod phase updated: running -> failed
	tc = generateKubernetesTestCaseTemplate()
	tc.pv.Status.Phase = apiv1.VolumeBound
	workloads = []types.WorkloadStatus{}
	for _, p := range tc.pods {
		ws := types.WorkloadStatus{
			PodName:      p.Name,
			PodStatus:    string(p.Status.Phase),
			WorkloadName: TestWorkloadName,
			WorkloadType: TestWorkloadKind,
		}
		workloads = append(workloads, ws)
	}
	tc.volume.Status.KubernetesStatus = types.KubernetesStatus{
		PVName:          TestPVName,
		PVStatus:        string(apiv1.VolumeBound),
		Namespace:       TestNamespace,
		PVCName:         TestPVCName,
		WorkloadsStatus: workloads,
	}
	tc.copyCurrentToExpect()
	workloads = []types.WorkloadStatus{}
	for _, p := range tc.pods {
		p.Status.Phase = apiv1.PodFailed
		ws := types.WorkloadStatus{
			PodName:      p.Name,
			PodStatus:    string(p.Status.Phase),
			WorkloadName: TestWorkloadName,
			WorkloadType: TestWorkloadKind,
		}
		workloads = append(workloads, ws)
	}
	tc.expectVolume.Status.KubernetesStatus.WorkloadsStatus = workloads
	testCases["pod phase updated to 'failed'"] = tc

	// pod deleted
	tc = generateKubernetesTestCaseTemplate()
	tc.pv.Status.Phase = apiv1.VolumeBound
	workloads = []types.WorkloadStatus{}
	for _, p := range tc.pods {
		p.DeletionTimestamp = &deleteTime
		ws := types.WorkloadStatus{
			PodName:      p.Name,
			PodStatus:    string(p.Status.Phase),
			WorkloadName: TestWorkloadName,
			WorkloadType: TestWorkloadKind,
		}
		workloads = append(workloads, ws)
	}
	tc.volume.Status.KubernetesStatus = types.KubernetesStatus{
		PVName:          TestPVName,
		PVStatus:        string(apiv1.VolumeBound),
		Namespace:       TestNamespace,
		PVCName:         TestPVCName,
		WorkloadsStatus: workloads,
	}
	tc.copyCurrentToExpect()
	tc.expectVolume.Status.KubernetesStatus.LastPodRefAt = getTestNow()
	testCases["pod deleted"] = tc

	// pv phase updated: bound -> failed
	tc = generateKubernetesTestCaseTemplate()
	tc.pv.Status.Phase = apiv1.VolumeFailed
	workloads = []types.WorkloadStatus{}
	for _, p := range tc.pods {
		ws := types.WorkloadStatus{
			PodName:      p.Name,
			PodStatus:    string(p.Status.Phase),
			WorkloadName: TestWorkloadName,
			WorkloadType: TestWorkloadKind,
		}
		workloads = append(workloads, ws)
	}
	tc.volume.Status.KubernetesStatus = types.KubernetesStatus{
		PVName:          TestPVName,
		PVStatus:        string(apiv1.VolumeBound),
		Namespace:       TestNamespace,
		PVCName:         TestPVCName,
		WorkloadsStatus: workloads,
	}
	tc.copyCurrentToExpect()
	tc.expectVolume.Status.KubernetesStatus.PVStatus = string(apiv1.VolumeFailed)
	tc.expectVolume.Status.KubernetesStatus.LastPVCRefAt = getTestNow()
	tc.expectVolume.Status.KubernetesStatus.LastPodRefAt = getTestNow()
	testCases["pv phase updated to 'failed'"] = tc

	// pv deleted
	tc = generateKubernetesTestCaseTemplate()
	tc.pv.Status.Phase = apiv1.VolumeBound
	tc.pv.DeletionTimestamp = &deleteTime
	workloads = []types.WorkloadStatus{}
	for _, p := range tc.pods {
		ws := types.WorkloadStatus{
			PodName:      p.Name,
			PodStatus:    string(p.Status.Phase),
			WorkloadName: TestWorkloadName,
			WorkloadType: TestWorkloadKind,
		}
		workloads = append(workloads, ws)
	}
	tc.volume.Status.KubernetesStatus = types.KubernetesStatus{
		Namespace:       TestNamespace,
		PVCName:         TestPVCName,
		WorkloadsStatus: workloads,
		LastPodRefAt:    "",
	}
	tc.copyCurrentToExpect()
	tc.expectVolume.Status.KubernetesStatus.LastPVCRefAt = getTestNow()
	tc.expectVolume.Status.KubernetesStatus.LastPodRefAt = getTestNow()
	testCases["pv deleted"] = tc

	// unknown PV - no CSI
	tc = generateKubernetesTestCaseTemplate()
	tc.pv.Spec.CSI = nil
	tc.pvc = nil
	tc.pods = nil
	tc.copyCurrentToExpect()
	testCases["unknown pv - no CSI"] = tc

	// unknown PV - wrong CSI driver
	tc = generateKubernetesTestCaseTemplate()
	tc.pv.Spec.CSI.Driver = "random_csi_driver"
	tc.pvc = nil
	tc.pods = nil
	tc.copyCurrentToExpect()
	testCases["unknown pv - wrong CSI driver"] = tc

	s.runKubernetesTestCases(c, testCases)
}

func (s *TestSuite) runKubernetesTestCases(c *C, testCases map[string]*KubernetesTestCase) {
	for name, tc := range testCases {
		var err error
		fmt.Printf("testing %v\n", name)

		kubeClient := fake.NewSimpleClientset()
		kubeInformerFactory := informers.NewSharedInformerFactory(kubeClient, controller.NoResyncPeriodFunc())

		lhClient := lhfake.NewSimpleClientset()
		lhInformerFactory := lhinformerfactory.NewSharedInformerFactory(lhClient, controller.NoResyncPeriodFunc())
		vIndexer := lhInformerFactory.Longhorn().V1beta1().Volumes().Informer().GetIndexer()

		pvIndexer := kubeInformerFactory.Core().V1().PersistentVolumes().Informer().GetIndexer()
		pvcIndexer := kubeInformerFactory.Core().V1().PersistentVolumeClaims().Informer().GetIndexer()
		pIndexer := kubeInformerFactory.Core().V1().Pods().Informer().GetIndexer()

		kc := newTestKubernetesPVController(lhInformerFactory, kubeInformerFactory, lhClient, kubeClient)

		// Need to create pv, pvc, pod and longhorn volume
		var v *longhorn.Volume
		if tc.volume != nil {
			v, err = lhClient.LonghornV1beta1().Volumes(TestNamespace).Create(tc.volume)
			c.Assert(err, IsNil)
			err = vIndexer.Add(v)
			c.Assert(err, IsNil)
		}

		var pv *apiv1.PersistentVolume
		if tc.pv != nil {
			pv, err = kubeClient.CoreV1().PersistentVolumes().Create(tc.pv)
			c.Assert(err, IsNil)
			pvIndexer.Add(pv)
			c.Assert(err, IsNil)
			if pv.DeletionTimestamp != nil {
				kc.enqueuePVDeletion(pv)
			}
		}

		if tc.pvc != nil {
			pvc, err := kubeClient.CoreV1().PersistentVolumeClaims(TestNamespace).Create(tc.pvc)
			c.Assert(err, IsNil)
			pvcIndexer.Add(pvc)
		}

		if len(tc.pods) != 0 {
			for _, p := range tc.pods {
				p, err = kubeClient.CoreV1().Pods(TestNamespace).Create(p)
				c.Assert(err, IsNil)
				pIndexer.Add(p)
			}
		}

		if pv != nil {
			err = kc.syncKubernetesStatus(getKey(pv, c))
			c.Assert(err, IsNil)
		}

		if v != nil {
			retV, err := lhClient.LonghornV1beta1().Volumes(TestNamespace).Get(v.Name, metav1.GetOptions{})
			c.Assert(err, IsNil)
			c.Assert(retV.Spec, DeepEquals, tc.expectVolume.Spec)
			sort.Slice(retV.Status.KubernetesStatus.WorkloadsStatus, func(i, j int) bool {
				return retV.Status.KubernetesStatus.WorkloadsStatus[i].PodName < retV.Status.KubernetesStatus.WorkloadsStatus[j].PodName
			})
			sort.Slice(tc.expectVolume.Status.KubernetesStatus.WorkloadsStatus, func(i, j int) bool {
				return tc.expectVolume.Status.KubernetesStatus.WorkloadsStatus[i].PodName < tc.expectVolume.Status.KubernetesStatus.WorkloadsStatus[j].PodName
			})
			c.Assert(retV.Status.KubernetesStatus, DeepEquals, tc.expectVolume.Status.KubernetesStatus)
		}

	}
}

func (s *TestSuite) TestDisasterRecovery(c *C) {
	deleteTime := metav1.Now()
	var workloads []types.WorkloadStatus
	var tc *DisasterRecoveryTestCase
	testCases := map[string]*DisasterRecoveryTestCase{}

	tc = generateDisasterRecoveryTestCaseTemplate()
	tc.volume.Status.State = types.VolumeStateDetached
	tc.pvc.Status.Phase = apiv1.ClaimBound
	tc.node = newNode(TestNode1, TestNamespace, true, types.ConditionStatusFalse, types.NodeConditionReasonKubernetesNodeGone)
	workloads = []types.WorkloadStatus{}
	for _, p := range tc.pods {
		p.Status.Phase = apiv1.PodPending
		ws := types.WorkloadStatus{
			PodName:      p.Name,
			PodStatus:    string(p.Status.Phase),
			WorkloadName: TestWorkloadName,
			WorkloadType: TestWorkloadKind,
		}
		workloads = append(workloads, ws)
	}
	tc.volume.Status.KubernetesStatus = types.KubernetesStatus{
		PVName:          TestPVName,
		PVStatus:        string(apiv1.VolumeBound),
		Namespace:       TestNamespace,
		PVCName:         TestPVCName,
		WorkloadsStatus: workloads,
		LastPodRefAt:    getTestNow(),
	}
	tc.vaShouldExist = false
	testCases["va StatefulSet deleted"] = tc

	tc = generateDisasterRecoveryTestCaseTemplate()
	tc.node = newNode(TestNode1, TestNamespace, true, types.ConditionStatusTrue, "")
	tc.pvc.Status.Phase = apiv1.ClaimBound
	workloads = []types.WorkloadStatus{}
	for _, p := range tc.pods {
		p.Status.Phase = apiv1.PodPending
		ws := types.WorkloadStatus{
			PodName:      p.Name,
			PodStatus:    string(p.Status.Phase),
			WorkloadName: TestWorkloadName,
			WorkloadType: TestWorkloadKind,
		}
		workloads = append(workloads, ws)
	}
	tc.volume.Status.KubernetesStatus = types.KubernetesStatus{
		PVName:          TestPVName,
		PVStatus:        string(apiv1.VolumeBound),
		Namespace:       TestNamespace,
		PVCName:         TestPVCName,
		WorkloadsStatus: workloads,
		LastPodRefAt:    getTestNow(),
	}
	tc.vaShouldExist = true
	testCases["va unchanged when node is Ready"] = tc

	// the associated 2 pods become Terminating. And user forces deleting one pod.
	tc = generateDisasterRecoveryTestCaseTemplate()
	tc.volume.Status.State = types.VolumeStateDetached
	tc.pvc.Status.Phase = apiv1.ClaimBound
	tc.node = newNode(TestNode1, TestNamespace, true, types.ConditionStatusFalse, types.NodeConditionReasonKubernetesNodeGone)
	workloads = []types.WorkloadStatus{}
	for _, p := range tc.pods {
		p.DeletionTimestamp = &deleteTime

	}
	pod2 := newPodWithPVC(TestPod2)
	pod2.Status.Phase = apiv1.PodPending
	tc.pods = append(tc.pods, pod2)
	for _, p := range tc.pods {
		if p.DeletionTimestamp == nil {
			ws := types.WorkloadStatus{
				PodName:      p.Name,
				PodStatus:    string(p.Status.Phase),
				WorkloadName: TestWorkloadName,
				WorkloadType: TestWorkloadKind,
			}
			workloads = append(workloads, ws)
		}
	}
	tc.volume.Status.KubernetesStatus = types.KubernetesStatus{
		PVName:          TestPVName,
		PVStatus:        string(apiv1.VolumeBound),
		Namespace:       TestNamespace,
		PVCName:         TestPVCName,
		WorkloadsStatus: workloads,
	}
	tc.vaShouldExist = true
	testCases["va StatefulSet retained when one terminating pod is not cleared"] = tc

	// the associated 2 pods become Terminating. And user forces deleting all pods.
	tc = generateDisasterRecoveryTestCaseTemplate()
	tc.volume.Status.State = types.VolumeStateDetached
	tc.pvc.Status.Phase = apiv1.ClaimBound
	tc.node = newNode(TestNode1, TestNamespace, true, types.ConditionStatusFalse, types.NodeConditionReasonKubernetesNodeGone)
	workloads = []types.WorkloadStatus{}
	tc.pods = append(tc.pods, newPodWithPVC(TestPod2))
	for _, p := range tc.pods {
		p.Status.Phase = apiv1.PodPending
	}
	for _, p := range tc.pods {
		ws := types.WorkloadStatus{
			PodName:      p.Name,
			PodStatus:    string(p.Status.Phase),
			WorkloadName: TestWorkloadName,
			WorkloadType: TestWorkloadKind,
		}
		workloads = append(workloads, ws)
	}
	tc.volume.Status.KubernetesStatus = types.KubernetesStatus{
		PVName:          TestPVName,
		PVStatus:        string(apiv1.VolumeBound),
		Namespace:       TestNamespace,
		PVCName:         TestPVCName,
		WorkloadsStatus: workloads,
	}
	tc.vaShouldExist = false
	testCases["va StatefulSet deleted when all terminating pods are cleared"] = tc

	// ReplicaSet allows for VA deletion while the pods are stuck in the terminating state
	tc = generateDisasterRecoveryTestCaseTemplate()
	tc.volume.Status.State = types.VolumeStateAttached
	tc.pvc.Status.Phase = apiv1.ClaimBound
	tc.node = newNode(TestNode1, TestNamespace, true, types.ConditionStatusFalse, types.NodeConditionReasonKubernetesNodeGone)
	workloads = []types.WorkloadStatus{}
	for _, p := range tc.pods {
		p.DeletionTimestamp = &deleteTime
	}

	pod2 = newPodWithPVC(TestPod2)
	pod2.Status.Phase = apiv1.PodPending
	tc.pods = append(tc.pods, pod2)
	for _, p := range tc.pods {

		// Change the pods to ReplicaSet before running
		var ref = p.OwnerReferences[0]
		ref.Name = TestWorkloadReplicaSet
		ref.Kind = TestReplicaSetKind
		p.OwnerReferences[0] = ref

		if p.DeletionTimestamp == nil {
			ws := types.WorkloadStatus{
				PodName:      p.Name,
				PodStatus:    string(p.Status.Phase),
				WorkloadName: TestWorkloadReplicaSet,
				WorkloadType: TestReplicaSetKind,
			}
			workloads = append(workloads, ws)
		}
	}
	tc.volume.Status.KubernetesStatus = types.KubernetesStatus{
		PVName:          TestPVName,
		PVStatus:        string(apiv1.VolumeBound),
		Namespace:       TestNamespace,
		PVCName:         TestPVCName,
		WorkloadsStatus: workloads,
	}
	tc.vaShouldExist = false
	testCases["va ReplicaSet deleted when one terminating pod is not cleared"] = tc

	s.runDisasterRecoveryTestCases(c, testCases)
}

func (s *TestSuite) runDisasterRecoveryTestCases(c *C, testCases map[string]*DisasterRecoveryTestCase) {
	for name, tc := range testCases {
		var err error
		fmt.Printf("testing %v\n", name)

		kubeClient := fake.NewSimpleClientset()
		kubeInformerFactory := informers.NewSharedInformerFactory(kubeClient, controller.NoResyncPeriodFunc())

		lhClient := lhfake.NewSimpleClientset()
		lhInformerFactory := lhinformerfactory.NewSharedInformerFactory(lhClient, controller.NoResyncPeriodFunc())
		vIndexer := lhInformerFactory.Longhorn().V1beta1().Volumes().Informer().GetIndexer()
		nodeIndexer := lhInformerFactory.Longhorn().V1beta1().Nodes().Informer().GetIndexer()

		pvIndexer := kubeInformerFactory.Core().V1().PersistentVolumes().Informer().GetIndexer()
		pvcIndexer := kubeInformerFactory.Core().V1().PersistentVolumeClaims().Informer().GetIndexer()
		pIndexer := kubeInformerFactory.Core().V1().Pods().Informer().GetIndexer()
		vaIndexer := kubeInformerFactory.Storage().V1beta1().VolumeAttachments().Informer().GetIndexer()

		kc := newTestKubernetesPVController(lhInformerFactory, kubeInformerFactory, lhClient, kubeClient)

		if tc.node != nil {
			node, err := lhClient.LonghornV1beta1().Nodes(TestNamespace).Create(tc.node)
			c.Assert(err, IsNil)
			nodeIndexer.Add(node)
		}

		var v *longhorn.Volume
		if tc.volume != nil {
			v, err = lhClient.LonghornV1beta1().Volumes(TestNamespace).Create(tc.volume)
			c.Assert(err, IsNil)
			err = vIndexer.Add(v)
			c.Assert(err, IsNil)
		}

		var pv *apiv1.PersistentVolume
		if tc.pv != nil {
			pv, err = kubeClient.CoreV1().PersistentVolumes().Create(tc.pv)
			c.Assert(err, IsNil)
			pvIndexer.Add(pv)
			c.Assert(err, IsNil)
			if pv.DeletionTimestamp != nil {
				kc.enqueuePVDeletion(pv)
			}
		}

		if tc.pvc != nil {
			pvc, err := kubeClient.CoreV1().PersistentVolumeClaims(TestNamespace).Create(tc.pvc)
			c.Assert(err, IsNil)
			pvcIndexer.Add(pvc)
		}

		if tc.va != nil {
			va, err := kubeClient.StorageV1beta1().VolumeAttachments().Create(tc.va)
			c.Assert(err, IsNil)
			vaIndexer.Add(va)
		}

		if len(tc.pods) != 0 {
			for _, p := range tc.pods {
				p, err = kubeClient.CoreV1().Pods(TestNamespace).Create(p)
				c.Assert(err, IsNil)
				pIndexer.Add(p)
			}
		}

		if pv != nil {
			err = kc.syncKubernetesStatus(getKey(pv, c))
			c.Assert(err, IsNil)
		}

		va, err := kubeClient.StorageV1beta1().VolumeAttachments().Get(TestVAName, metav1.GetOptions{})
		if tc.vaShouldExist {
			c.Assert(err, IsNil)
			c.Assert(va, DeepEquals, tc.va)
		} else {
			if err != nil {
				c.Assert(datastore.ErrorIsNotFound(err), Equals, true)
			} else {
				c.Assert(va.DeletionTimestamp, NotNil)
			}
		}
	}
}
